// Package reportes responde preguntas analíticas y agregadas sobre la operación
// y NO es dueño de ningún hecho (reportes.md (a)). Mantiene un modelo de lectura
// derivado —proyecciones propias alimentadas por los eventos versionados de sus
// productores (ADR 18, cerrado)— bajo su propio esquema, rol y compuerta de
// migración. Si una proyección discrepa de su origen, gana el origen
// (reportes.md (e), (h) R5).
//
// Capa de soporte. Vive en el deployable `core` (00, tabla de topología). Su eje
// de escalamiento es complejidad de consulta y memoria; la forma de instancia
// con más memoria no se puede satisfacer en V1 mientras viva en `core`
// (reportes.md (k), ADR 17), y se atiende el día de la extracción.
//
// Aristas síncronas salientes: hacia `catalogos` (lectura por lote y a una
// fecha, reportes.md (f)) y la restringida a `identidad` para revocación
// (listarRevocadas + obtenerSesion, DEC-051). NO llama a `operacion` ni a
// `procesamiento`: esos hechos llegan por evento. Arista entrante: solo desde
// `bff` (P16).
//
// Consumidor terminal: no publica eventos, no tiene `outbox` ni relay
// (reportes.md (g)).
//
// Scaffolding inicial: el cableado y la estructura existen; la lógica de negocio
// real todavía no. Varias superficies están bloqueadas por brechas de contrato
// (ver README, R3–R12): sus rutas, tablas y métodos no se montan hasta que el
// contrato las exponga.
package reportes

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/auth"
	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
	"github.com/AI27-Tech/pf-svc-reportes/internal/consumidores"
	"github.com/AI27-Tech/pf-svc-reportes/internal/proyecciones"
	"github.com/AI27-Tech/pf-svc-reportes/internal/proyeccionpg"
)

// autorizarLectura deja leer reportes solo a la audiencia Interno: el cliente no
// accede al dashboard interno (DEC-022 #9) y recibe 403, igual que cualquier
// audiencia que no sea Interno. Sin claims es 401. Interno no se acota por su
// tenant (DEC-129), así que quien pasa lee todos los clientes (tenant nil).
func autorizarLectura(ctx context.Context) error {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil {
		return platformerrors.NewNoAutorizado("reportes: petición sin autenticar")
	}
	if claims.Audiencia != auth.AudienciaInterno {
		return platformerrors.NewProhibido("reportes: solo la audiencia Interno lee reportes (DEC-022 #9)")
	}
	return nil
}

// Pools agrupa los pools por rol que el módulo necesita. reportes usa un solo
// esquema y un solo rol (`svc_reportes`), así que recibe un único pool (00 regla
// 4, ADR 18 §1). Nunca comparte pool entre roles.
type Pools struct {
	Reportes *pgxpool.Pool
}

// Deps agrupa los puertos de salida que la raíz resuelve por el consumidor.
type Deps struct {
	// Catalogos es la arista a catalogos: lectura por lote y a una fecha de las
	// dimensiones (reportes.md (f)). Puede ser nil en el esqueleto; sin ella las
	// proyecciones que necesiten dimensión quedan sin resolver.
	Catalogos CatalogosPort
	// Revocacion es la arista restringida a identidad (DEC-051). Opcional en el
	// esqueleto; sin ella la autorización corre sin proyección de tombstones.
	Revocacion RevocacionPort
	// Subscriber es el bus durable del que reportes consume los eventos viaje.*
	// para poblar sus proyecciones (ADR 29). Nil en arranque standalone sin bus:
	// en ese caso Workers() no arranca el consumidor de proyecciones.
	Subscriber bus.Subscriber
	// TechoFrescura es el umbral de retraso a partir del cual un reporte se marca
	// `stale` (reportes.md (i)). Es configuración del servicio, no del contrato.
	// Cero o negativo desactiva la marca (nunca stale): default de desarrollo, no
	// una decisión de producto.
	TechoFrescura time.Duration
}

// Module es la instancia construida del módulo reportes.
type Module struct {
	pools  Pools
	deps   Deps
	lector *proyecciones.Lector
	// consumidor de proyecciones (viaje.* -> hecho_*). Nil sin Subscriber
	// (standalone sin bus); en ese caso Workers() no lo devuelve.
	consumidorProyecciones worker.Worker
}

// tiposViajeConProductor son los eventos viaje.* que operacion publica hoy y que
// reportes proyecta (E6-T1). Los otros tipos registrados en el consumidor
// (viaje.cerrado, condicion_abierta/_cerrada) no tienen productor aún: no se
// suscriben para no abrir un durable que nunca recibe.
var tiposViajeConProductor = []string{
	"viaje.creado",
	"viaje.estado_cambiado",
	"viaje.asignado",
	"viaje.reasignado",
}

// New recibe el pool de svc_reportes y los puertos ya resueltos. No lee entorno
// (00 regla 4): la raíz cablea todo. Si Deps.Subscriber está presente, construye
// el consumidor durable de proyecciones sobre el pool de svc_reportes y lo deja
// listo para que Workers() lo devuelva.
func New(p Pools, d Deps) (*Module, error) {
	m := &Module{pools: p, deps: d}
	m.lector = proyecciones.NewLector(p.Reportes, d.TechoFrescura)

	if d.Subscriber != nil {
		ad := proyeccionpg.New(p.Reportes, slog.Default())
		cons := consumidores.NewConsumidorProyecciones(ad, ad, ad)
		// El handler decodifica el Sobre y lo procesa. Un evento venenoso ya quedó
		// en cuarentena del consumidor (ErrEventoVenenoso): se acusa (nil) para no
		// reintentarlo en el bus. Cualquier otro error se propaga para que el bus
		// reintente y, agotados los reintentos, lo cuarentene (ADR 29): así el
		// borde de arranque en frío (estado_cambiado sin creado) se reintenta y
		// termina en cuarentena en vez de ponchar un tenant.
		handler := func(ctx context.Context, s bus.Sobre) error {
			err := cons.Procesar(ctx, proyeccionpg.Decodificar(s))
			if errors.Is(err, consumidores.ErrEventoVenenoso) {
				return nil
			}
			return err
		}
		w, err := d.Subscriber.Subscribe("reportes", bus.SensibilidadAmplia, tiposViajeConProductor, handler)
		if err != nil {
			return nil, err
		}
		m.consumidorProyecciones = w
	}

	return m, nil
}

// Routes monta las rutas HTTP del módulo bajo /api/v1/reportes/... en el mux
// dado. Scaffolding: hoy NO monta ninguna ruta de reporte. Las operaciones
// reporteViajes y reporteEstados existen en el contrato (R2), pero la MARCA DE
// FRESCURA no está en sus schemas (R3) y la frescura no es opcional
// (reportes.md (i)): sin ella no se monta la ruta. Las demás rutas
// (/cumplimiento, /flota, /exportaciones) no existen en el contrato (R4–R6).
func (m *Module) Routes(mux *http.ServeMux) {
	q := m.Consultas()

	// GET /api/v1/reportes/viajes?desde&hasta&en_curso — reporte de viajes. Sin
	// en_curso, desde y hasta son obligatorios; con en_curso=true son opcionales.
	// Esa regla la valida la consulta, que también sirve a la fachada en proceso.
	mux.HandleFunc("GET /api/v1/reportes/viajes", func(w http.ResponseWriter, r *http.Request) {
		params, err := parsearViajes(r)
		if err != nil {
			escribirError(w, err)
			return
		}
		rep, err := q.ReporteViajes(r.Context(), params)
		if err != nil {
			escribirError(w, err)
			return
		}
		escribirJSON(w, http.StatusOK, rep)
	})

	// GET /api/v1/reportes/estados?desde&hasta — distribución por estado.
	mux.HandleFunc("GET /api/v1/reportes/estados", func(w http.ResponseWriter, r *http.Request) {
		desde, hasta, err := parsearPeriodo(r)
		if err != nil {
			escribirError(w, err)
			return
		}
		rep, err := q.ReporteEstados(r.Context(), repclient.ReporteEstadosParams{Desde: desde, Hasta: hasta})
		if err != nil {
			escribirError(w, err)
			return
		}
		escribirJSON(w, http.StatusOK, rep)
	})

	// Las demás rutas (/cumplimiento, /flota, /exportaciones) no existen en el
	// contrato (R4–R6): no se montan.
}

// parsearViajes lee desde, hasta (RFC 3339) y en_curso de reporteViajes. Los tres
// son opcionales aquí: que falte el rango sin en_curso lo valida la consulta. Un
// formato inválido es validación (400), no un error de servidor.
func parsearViajes(r *http.Request) (repclient.ReporteViajesParams, error) {
	var p repclient.ReporteViajesParams
	q := r.URL.Query()
	var err error
	if p.Desde, err = fechaOpcional(q.Get("desde"), "desde"); err != nil {
		return p, err
	}
	if p.Hasta, err = fechaOpcional(q.Get("hasta"), "hasta"); err != nil {
		return p, err
	}
	if v := q.Get("en_curso"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return p, platformerrors.NewValidacion("en_curso inválido (se espera true o false)")
		}
		p.EnCurso = &b
	}
	return p, nil
}

// fechaOpcional parsea una fecha RFC 3339; un valor vacío es «no enviada» (nil).
func fechaOpcional(v, nombre string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, platformerrors.NewValidacion("%s inválido (se espera RFC 3339)", nombre)
	}
	return &t, nil
}

// parsearPeriodo lee los query params obligatorios desde/hasta (RFC 3339) y
// valida que formen un rango. Un formato inválido o un rango invertido es
// validación (400), no un error de servidor.
func parsearPeriodo(r *http.Request) (desde, hasta time.Time, err error) {
	desde, err = time.Parse(time.RFC3339, r.URL.Query().Get("desde"))
	if err != nil {
		return desde, hasta, platformerrors.NewValidacion("desde inválido (se espera RFC 3339)")
	}
	hasta, err = time.Parse(time.RFC3339, r.URL.Query().Get("hasta"))
	if err != nil {
		return desde, hasta, platformerrors.NewValidacion("hasta inválido (se espera RFC 3339)")
	}
	if hasta.Before(desde) {
		return desde, hasta, platformerrors.NewValidacion("hasta no puede ser anterior a desde")
	}
	return desde, hasta, nil
}

// escribirJSON serializa v como JSON con el status dado.
func escribirJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// escribirError traduce un error de la taxonomía de la plataforma a su status
// HTTP (invariante I3: la misma categoría se traduce igual en HTTP y en proceso).
func escribirError(w http.ResponseWriter, err error) {
	cat := platformerrors.AsCategory(err)
	escribirJSON(w, cat.HTTPStatus(), repclient.Error{
		Codigo:  codigoDe(cat),
		Mensaje: err.Error(),
	})
}

// codigoDe mapea la categoría de la plataforma a un código de máquina estable en
// snake_case (los consumidores ramifican sobre `codigo`, no sobre `mensaje`).
func codigoDe(c platformerrors.Category) string {
	switch c {
	case platformerrors.CategoryNoAutorizado:
		return "no_autorizado"
	case platformerrors.CategoryProhibido:
		return "prohibido"
	case platformerrors.CategoryNoEncontrado:
		return "no_encontrado"
	case platformerrors.CategoryValidacion:
		return "validacion"
	case platformerrors.CategoryConflicto:
		return "conflicto"
	case platformerrors.CategoryDegradado:
		return "degradado"
	case platformerrors.CategoryRateLimit:
		return "rate_limit"
	default:
		return "roto"
	}
}

// Workers devuelve los workers embebidos del módulo (ADR 29 §6), cada uno con el
// rol y pool de svc_reportes. En E6-T1 devuelve el consumidor durable de
// proyecciones cuando la raíz inyectó un Subscriber (Deps.Subscriber); sin bus
// (arranque standalone) la lista va vacía.
//
// Pendientes (no en este slice): el consumidor de sesion.revocada (suscripción
// durable propia, DEC-057) y el reconciliador (R12).
func (m *Module) Workers() []worker.Worker {
	var ws []worker.Worker
	if m.consumidorProyecciones != nil {
		ws = append(ws, m.consumidorProyecciones)
	}
	return ws
}

// Consultas expone los casos de uso que bff consume en proceso (arista síncrona
// entrante). Tipada SOLO con tipos de pf-pkg-contracts, nunca con tipos de
// internal/.
func (m *Module) Consultas() ConsultasAPI {
	return &consultas{m: m}
}

// ConsultasAPI es la superficie síncrona de reportes. Cada método corresponde
// 1:1 a un operationId presente en reportes.yaml de la versión fijada (00 regla
// 6): reporteViajes y reporteEstados. Tipada con los tipos generados del
// paquete reportes de pf-pkg-contracts.
//
// Las dos respuestas incluyen la marca de frescura (reportes.md (i)):
// proyeccion_al (hasta qué evento aplicó la proyección) y stale (si el retraso
// excede el techo configurado). tracer.10 añadió esos campos al contrato, que
// era la brecha R3 que antes bloqueaba estas lecturas.
type ConsultasAPI interface {
	ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error)
	ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error)
}

type consultas struct {
	m *Module
}

func (c *consultas) ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error) {
	if err := autorizarLectura(ctx); err != nil {
		return nil, err
	}
	// Sin en_curso el rango completo es obligatorio; con en_curso es opcional y,
	// si viene, se interseca. La regla vive aquí y no en el handler para que la
	// fachada en proceso (core) la cumpla igual que HTTP.
	enCurso := params.EnCurso != nil && *params.EnCurso
	if !enCurso && (params.Desde == nil || params.Hasta == nil) {
		return nil, platformerrors.NewValidacion("reportes: sin en_curso, desde y hasta son obligatorios")
	}
	if params.Desde != nil && params.Hasta != nil && params.Hasta.Before(*params.Desde) {
		return nil, platformerrors.NewValidacion("hasta no puede ser anterior a desde")
	}
	filas, fr, err := c.m.lector.Viajes(ctx, nil, params.Desde, params.Hasta, enCurso)
	if err != nil {
		return nil, err
	}
	out := make([]repclient.FilaViaje, 0, len(filas))
	for _, f := range filas {
		fila := repclient.FilaViaje{Estado: f.Estado}
		if err := fila.ViajeId.UnmarshalText([]byte(f.ViajeID)); err != nil {
			return nil, platformerrors.NewRoto("reportes: viaje_id no es UUID en la proyección: %v", err)
		}
		if f.ClienteID != "" {
			var cid repclient.Id
			if err := cid.UnmarshalText([]byte(f.ClienteID)); err != nil {
				return nil, platformerrors.NewRoto("reportes: cliente_id no es UUID en la proyección: %v", err)
			}
			fila.ClienteId = &cid
		}
		creado := f.CreadoEn
		fila.CreadoEn = &creado
		fila.CerradoEn = f.CerradoEn
		out = append(out, fila)
	}
	rep := &repclient.ReporteViajes{Filas: out, Total: len(out)}
	aplicarFrescura(&rep.ProyeccionAl, &rep.Stale, fr)
	return rep, nil
}

func (c *consultas) ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error) {
	if err := autorizarLectura(ctx); err != nil {
		return nil, err
	}
	conteos, fr, err := c.m.lector.Estados(ctx, nil, params.Desde, params.Hasta)
	if err != nil {
		return nil, err
	}
	out := make([]repclient.ConteoEstado, 0, len(conteos))
	for _, ce := range conteos {
		out = append(out, repclient.ConteoEstado{
			Estado:                 ce.Estado,
			Conteo:                 ce.Conteo,
			PermanenciaPromedioSeg: ce.PermanenciaPromSeg,
		})
	}
	rep := &repclient.ReporteEstados{Conteos: out}
	aplicarFrescura(&rep.ProyeccionAl, &rep.Stale, fr)
	return rep, nil
}

// aplicarFrescura vuelca la frescura del caso de uso a los dos campos del
// contrato. proyeccion_al queda nil (JSON null) si la proyección no aplicó
// ningún evento del tenant; stale se envía siempre (false cuando no hay retraso).
func aplicarFrescura(proyeccionAl **repclient.MarcaTiempo, stale **bool, fr proyecciones.Frescura) {
	if !fr.AplicadoHasta.IsZero() {
		al := fr.AplicadoHasta
		*proyeccionAl = &al
	}
	s := fr.Stale
	*stale = &s
}
