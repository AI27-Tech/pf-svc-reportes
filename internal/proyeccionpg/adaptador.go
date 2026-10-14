package proyeccionpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/AI27-Tech/pf-pkg-platform-go/inbox"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-svc-reportes/internal/consumidores"
	"github.com/jackc/pgx/v5"
)

const consumidor = "proyecciones"

// ErrTenantDesconocido marca un viaje.estado_cambiado de un viaje cuyo
// viaje.creado nunca se proyectó: no hay fila de la cual leer el tenant, y
// ponchar un valor viola P9. El consumidor propaga este error al bus, que
// reintenta y, agotados los reintentos, manda el sobre a cuarentena (ADR 29).
// Es un caso de arranque en frío que la reconstrucción (ADR 30, R12) resolverá;
// hoy se cuarentena de forma observable, no se traga.
var ErrTenantDesconocido = errors.New("proyeccionpg: estado_cambiado sin hecho_viaje previo del cual derivar el tenant")

// Adaptador implementa consumidores.Proyeccion, consumidores.Inbox y
// consumidores.DLQ contra el pool de svc_reportes.
//
// Atomicidad: Aplicar abre UNA transacción, escribe la proyección y registra el
// evento_id en inbox_evento en esa misma tx, y commitea. Así "aplicar + marcar"
// son atómicos aunque ConsumidorProyecciones.Procesar los exponga como dos
// llamadas: Marcar queda como no-op porque el alta del inbox ya ocurrió dentro
// de Aplicar. La idempotencia la da la PK de inbox_evento (inbox.Registrar
// devuelve ErrYaProcesado ante un duplicado), no lógica de aplicación.
type Adaptador struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New construye el adaptador con el pool de svc_reportes y un logger para la
// cuarentena. El logger puede ser nil (usa el default de slog).
func New(pool *pgxpool.Pool, log *slog.Logger) *Adaptador {
	if log == nil {
		log = slog.Default()
	}
	return &Adaptador{pool: pool, log: log}
}

// --- consumidores.Inbox ---

// YaProcesado consulta si el evento_id ya está en inbox_evento. Procesar lo usa
// para omitir un duplicado antes de reaplicar SQL; el alta real y su atomicidad
// con la proyección viven en Aplicar.
func (a *Adaptador) YaProcesado(ctx context.Context, eventoID string) (bool, error) {
	var existe bool
	err := a.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM reportes.inbox_evento WHERE evento_id = $1)`,
		eventoID,
	).Scan(&existe)
	if err != nil {
		return false, fmt.Errorf("proyeccionpg: consultar inbox: %w", err)
	}
	return existe, nil
}

// Marcar es no-op: Aplicar ya registró el evento_id en inbox_evento dentro de su
// propia tx, atómicamente con el efecto de dominio. Un segundo INSERT aquí
// chocaría con la PK.
func (a *Adaptador) Marcar(ctx context.Context, eventoID, tipo, consumidor string) error {
	return nil
}

// --- consumidores.DLQ ---

// Cuarentena registra un evento venenoso con solo metadatos sanitizados
// (evento_id, tipo, motivo), nunca el payload (ADR 29 §4). La cuarentena de
// transporte la lleva el bus por módulo (platform-go); el consumidor solo la
// alcanza para un tipo fuera del registro, que la suscripción filtrada por tipo
// hace casi inalcanzable. Por eso aquí se observa en log y no se persiste una
// tabla propia en este slice.
func (a *Adaptador) Cuarentena(ctx context.Context, eventoID, tipo, motivo string) error {
	a.log.Warn("evento en cuarentena del consumidor de proyecciones",
		slog.String("evento_id", eventoID),
		slog.String("tipo", tipo),
		slog.String("motivo", motivo),
	)
	return nil
}

// --- consumidores.Proyeccion ---

// Aplicar proyecta el evento según su tipo dentro de una sola tx, registrando el
// inbox en la misma tx. Solo los 4 tipos con productor (creado, estado_cambiado,
// asignado, reasignado) tienen efecto; cualquier otro tipo registrado llega aquí
// solo si el filtro de suscripción cambia, y se trata como no-aplicable.
func (a *Adaptador) Aplicar(ctx context.Context, e consumidores.Evento) error {
	raw, err := payloadDe(e)
	if err != nil {
		return err
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("proyeccionpg: abrir tx: %w", err)
	}
	defer tx.Rollback(ctx)

	switch e.Tipo {
	case "viaje.creado":
		err = aplicarCreado(ctx, tx, raw)
	case "viaje.estado_cambiado":
		err = aplicarEstadoCambiado(ctx, tx, e.EventoID, raw)
	case "viaje.asignado", "viaje.reasignado":
		err = aplicarAsignacion(ctx, tx, raw)
	default:
		return fmt.Errorf("proyeccionpg: tipo sin proyección en E6-T1: %s", e.Tipo)
	}
	if err != nil {
		return err
	}

	// Inbox en la MISMA tx: si falla el alta, la proyección se revierte. Un
	// duplicado (ErrYaProcesado) no debería llegar aquí porque Procesar consulta
	// YaProcesado antes; si llega por una carrera, se trata como no-op y la tx
	// revierte sin efecto doble.
	if err := inbox.Registrar(ctx, tx, e.EventoID, e.Tipo, consumidor); err != nil {
		if errors.Is(err, inbox.ErrYaProcesado) {
			return nil // la tx revierte vía defer; sin doble efecto
		}
		return fmt.Errorf("proyeccionpg: registrar inbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("proyeccionpg: commit: %w", err)
	}
	return nil
}

// --- mapeo evento -> fila, por tipo (claves del payload = las que opera
// serializa a mano, ver internal/viajes y internal/asignacion de operacion) ---

type payloadCreado struct {
	ViajeID   string    `json:"viaje_id"`
	ClienteID string    `json:"cliente_id"`
	Estado    string    `json:"estado"`
	CreadoEn  time.Time `json:"creado_en"`
}

// aplicarCreado inserta la fila del viaje. tenant_id = cliente_id (DEC-495,
// DEC-049: cliente = tenant). aplicado_hasta = creado_en (frescura interna).
// ON CONFLICT DO NOTHING: reproyectar un creado no debe fallar ni duplicar.
func aplicarCreado(ctx context.Context, tx pgx.Tx, raw json.RawMessage) error {
	var p payloadCreado
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("proyeccionpg: decodificar viaje.creado: %w", err)
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO reportes.hecho_viaje
		   (viaje_id, tenant_id, cliente_id, estado_actual, creado_en, aplicado_hasta)
		 VALUES ($1, $2, $3, $4, $5, $5)
		 ON CONFLICT (viaje_id) DO NOTHING`,
		p.ViajeID, p.ClienteID, p.ClienteID, p.Estado, p.CreadoEn,
	)
	if err != nil {
		return fmt.Errorf("proyeccionpg: insertar hecho_viaje: %w", err)
	}
	return nil
}

type payloadEstadoCambiado struct {
	ViajeID    string    `json:"viaje_id"`
	DeEstado   string    `json:"de_estado"`
	AEstado    string    `json:"a_estado"`
	CambiadoEn time.Time `json:"cambiado_en"`
}

// aplicarEstadoCambiado actualiza el estado del viaje e inserta la transición.
// El tenant de la transición se LEE de hecho_viaje (misma tx): el orden por
// agregado del bus garantiza que creado se proyectó antes. Si no hay fila,
// devuelve ErrTenantDesconocido (borde de arranque en frío) para que el bus lo
// cuarentene en vez de ponchar un tenant.
func aplicarEstadoCambiado(ctx context.Context, tx pgx.Tx, eventoID string, raw json.RawMessage) error {
	var p payloadEstadoCambiado
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("proyeccionpg: decodificar viaje.estado_cambiado: %w", err)
	}

	var tenant string
	err := tx.QueryRow(ctx,
		`UPDATE reportes.hecho_viaje
		    SET estado_actual = $2, aplicado_hasta = $3
		  WHERE viaje_id = $1
		 RETURNING tenant_id`,
		p.ViajeID, p.AEstado, p.CambiadoEn,
	).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTenantDesconocido
	}
	if err != nil {
		return fmt.Errorf("proyeccionpg: actualizar estado de hecho_viaje: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO reportes.hecho_transicion
		   (viaje_id, tenant_id, de_estado, a_estado, ocurrido_en, evento_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		p.ViajeID, tenant, nullSi(p.DeEstado), p.AEstado, p.CambiadoEn, eventoID,
	)
	if err != nil {
		return fmt.Errorf("proyeccionpg: insertar hecho_transicion: %w", err)
	}
	return nil
}

type payloadAsignacion struct {
	ViajeID     string   `json:"viaje_id"`
	UnidadesIDs []string `json:"unidades_ids"`
}

// aplicarAsignacion vuelca la escolta a hecho_viaje.unidades_ids (opción a:
// la lista tal como llega; ADR 31 / DEC-044). Sirve a viaje.asignado y
// viaje.reasignado por igual: ambos mandan la escolta COMPLETA resultante, así
// que es un reemplazo del arreglo, no un append. Si el viaje no está proyectado
// (sin creado previo), no actualiza nada y falla con ErrTenantDesconocido, el
// mismo borde de arranque en frío que estado_cambiado: se cuarentena, no se
// crea una fila a medias.
func aplicarAsignacion(ctx context.Context, tx pgx.Tx, raw json.RawMessage) error {
	var p payloadAsignacion
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("proyeccionpg: decodificar asignación: %w", err)
	}
	ct, err := tx.Exec(ctx,
		`UPDATE reportes.hecho_viaje SET unidades_ids = $2 WHERE viaje_id = $1`,
		p.ViajeID, p.UnidadesIDs,
	)
	if err != nil {
		return fmt.Errorf("proyeccionpg: actualizar unidades de hecho_viaje: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTenantDesconocido // asignación de un viaje sin creado proyectado
	}
	return nil
}

// nullSi devuelve nil para la cadena vacía, de modo que de_estado (nullable) se
// guarde NULL y no "" cuando el evento no trae estado previo.
func nullSi(s string) any {
	if s == "" {
		return nil
	}
	return s
}
