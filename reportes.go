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
	"errors"
	"log/slog"
	"net/http"

	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
	"github.com/AI27-Tech/pf-svc-reportes/internal/consumidores"
	"github.com/AI27-Tech/pf-svc-reportes/internal/proyeccionpg"
)

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
}

// Module es la instancia construida del módulo reportes.
type Module struct {
	pools Pools
	deps  Deps
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
	// TODO(reportes): montar reporteViajes y reporteEstados cuando R3 (frescura
	// contractual) exista, delegando en los mismos casos de uso que Consultas()
	// (I3). No montar /cumplimiento, /flota ni /exportaciones hasta R4–R6.
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
// IMPORTANTE: aunque las dos operaciones existen en el contrato (R2), sus
// schemas de respuesta (ReporteViajes, ReporteEstados) NO incluyen la marca de
// frescura (reportes.md (i)), que es obligatoria y el contrato todavía no
// declara (R3). Por eso los métodos se exponen pero devuelven "no implementado"
// hasta R3; el caso de uso interno ya se estructura para producir frescura en su
// resultado, y la ruta no se monta (ver Routes).
type ConsultasAPI interface {
	ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error)
	ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error)
}

type consultas struct {
	m *Module
}

func (c *consultas) ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error) {
	// Bloqueado por R3: la marca de frescura no está en el schema de respuesta.
	return nil, errNoImplementado("ReporteViajes")
}

func (c *consultas) ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error) {
	// Bloqueado por R3: la marca de frescura no está en el schema de respuesta.
	return nil, errNoImplementado("ReporteEstados")
}
