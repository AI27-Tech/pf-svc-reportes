// Package proyecciones aloja los casos de uso de lectura de reportes, uno por
// proyección V1 (hecho_viaje, hecho_transicion). Cada caso de uso devuelve,
// junto con sus datos, la MARCA DE FRESCURA de las proyecciones que usó —hasta
// qué evento se aplicó (reportes.md (i))—: la frescura es parte del resultado,
// no un detalle interno. Sin ella, un reporte congelado por caída del broker se
// vería igual que uno de un periodo tranquilo.
//
// Todo caso de uso filtra por tenant ANTES de agregar (P9): un conteo o promedio
// sobre dos tenants filtra sin devolver una sola fila ajena. Ninguna consulta es
// libre: el llamador envía parámetros a reportes definidos, nunca SQL.
package proyecciones

import (
	"context"
	"fmt"
	"time"

	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
)

// Frescura declara hasta qué punto está al día una proyección (reportes.md (i)).
// Es parte de todo resultado de reporte, no un campo opcional posterior.
type Frescura struct {
	// AplicadoHasta es el instante del último evento aplicado a la proyección en
	// el rango consultado. Cero (time.Time{}) si el rango no tocó ninguna fila:
	// el llamador lo traduce a `proyeccion_al: null` del contrato.
	AplicadoHasta time.Time
	// Stale indica si la proyección quedó atrás del techo de frescura: now menos
	// AplicadoHasta excede el umbral configurado. Si AplicadoHasta es cero (sin
	// datos), no es stale: no hay retraso que medir, solo ausencia de eventos.
	Stale bool
}

// FilaViaje es una fila del reporte de viajes, en la forma que el caso de uso
// produce. El adaptador HTTP la mapea al tipo generado del contrato.
type FilaViaje struct {
	ViajeID   string
	ClienteID string
	Estado    string
	CreadoEn  time.Time
	CerradoEn *time.Time
}

// ConteoEstado es un renglón del reporte de estados: cuántos viajes hay en un
// estado y su permanencia promedio en segundos (nil si no se puede calcular).
type ConteoEstado struct {
	Estado             string
	Conteo             int
	PermanenciaPromSeg *int
}

// Lector ejecuta las consultas de lectura sobre el pool de svc_reportes. Zero
// value no usable: construir con NewLector. El techo de frescura se inyecta
// porque es configuración del servicio, no del contrato (reportes.md (i)): el
// contrato expone el booleano `stale` ya resuelto, no el umbral.
type Lector struct {
	pool          *pgxpool.Pool
	techoFrescura time.Duration
	ahora         func() time.Time // inyectable para pruebas deterministas
}

// NewLector crea el lector con el pool y el techo de frescura. Un techo <= 0
// desactiva la marca stale (nunca marca atrasado): útil en desarrollo, no es una
// decisión de producto.
func NewLector(pool *pgxpool.Pool, techoFrescura time.Duration) *Lector {
	return &Lector{pool: pool, techoFrescura: techoFrescura, ahora: time.Now}
}

// Viajes lista los viajes creados en [desde, hasta] del tenant dado, con la
// frescura de la proyección hecho_viaje. Filtra por tenant ANTES de agregar
// (P9): el tenant es un parámetro del WHERE, nunca se agrega sobre filas ajenas.
func (l *Lector) Viajes(ctx context.Context, tenantID string, desde, hasta time.Time) ([]FilaViaje, Frescura, error) {
	filas, err := l.pool.Query(ctx,
		`SELECT viaje_id, cliente_id, estado_actual, creado_en, cerrado_en
		   FROM reportes.hecho_viaje
		  WHERE tenant_id = $1 AND creado_en >= $2 AND creado_en <= $3
		  ORDER BY creado_en`,
		tenantID, desde, hasta,
	)
	if err != nil {
		return nil, Frescura{}, fmt.Errorf("proyecciones: consultar viajes: %w", err)
	}
	defer filas.Close()

	var out []FilaViaje
	for filas.Next() {
		var f FilaViaje
		if err := filas.Scan(&f.ViajeID, &f.ClienteID, &f.Estado, &f.CreadoEn, &f.CerradoEn); err != nil {
			return nil, Frescura{}, fmt.Errorf("proyecciones: leer fila de viaje: %w", err)
		}
		out = append(out, f)
	}
	if err := filas.Err(); err != nil {
		return nil, Frescura{}, fmt.Errorf("proyecciones: recorrer viajes: %w", err)
	}

	fr, err := l.frescura(ctx, tenantID)
	if err != nil {
		return nil, Frescura{}, err
	}
	return out, fr, nil
}

// Estados devuelve la distribución por estado y la permanencia promedio de las
// transiciones ocurridas en [desde, hasta] del tenant, con la frescura de la
// proyección. El conteo es por estado destino de las transiciones del periodo.
func (l *Lector) Estados(ctx context.Context, tenantID string, desde, hasta time.Time) ([]ConteoEstado, Frescura, error) {
	filas, err := l.pool.Query(ctx,
		`SELECT a_estado, COUNT(*)
		   FROM reportes.hecho_transicion
		  WHERE tenant_id = $1 AND ocurrido_en >= $2 AND ocurrido_en <= $3
		  GROUP BY a_estado
		  ORDER BY a_estado`,
		tenantID, desde, hasta,
	)
	if err != nil {
		return nil, Frescura{}, fmt.Errorf("proyecciones: consultar estados: %w", err)
	}
	defer filas.Close()

	var out []ConteoEstado
	for filas.Next() {
		var c ConteoEstado
		if err := filas.Scan(&c.Estado, &c.Conteo); err != nil {
			return nil, Frescura{}, fmt.Errorf("proyecciones: leer conteo de estado: %w", err)
		}
		out = append(out, c)
	}
	if err := filas.Err(); err != nil {
		return nil, Frescura{}, fmt.Errorf("proyecciones: recorrer estados: %w", err)
	}

	// permanencia_promedio_seg queda nil en V1: la proyección hecho_transicion
	// no guarda la duración en cada estado, solo el instante de la transición, y
	// calcularla exige emparejar transiciones consecutivas por viaje. shortcut:
	// se omite hasta que el reporte lo pida; el campo es nullable en el contrato.

	fr, err := l.frescura(ctx, tenantID)
	if err != nil {
		return nil, Frescura{}, err
	}
	return out, fr, nil
}

// frescura calcula hasta cuándo aplicó eventos la proyección hecho_viaje del
// tenant, y si está atrasada respecto del techo. Toma el MAX(aplicado_hasta):
// es el instante de dominio del último evento que tocó una fila del tenant.
//
// Vale para ambos reportes, incluido el de estados que lee hecho_transicion:
// aplicarEstadoCambiado actualiza hecho_viaje.aplicado_hasta = cambiado_en en la
// misma proyección de cada transición (ver proyeccionpg/adaptador.go), así que
// MAX(aplicado_hasta) refleja también la última transición aplicada. No se lee
// la frescura de hecho_transicion porque esa tabla no tiene columna de frescura
// (su marca por fila es ocurrido_en, el instante de la transición, no "hasta
// cuándo aplicó la proyección").
func (l *Lector) frescura(ctx context.Context, tenantID string) (Frescura, error) {
	var aplicadoHasta *time.Time
	err := l.pool.QueryRow(ctx,
		`SELECT MAX(aplicado_hasta) FROM reportes.hecho_viaje WHERE tenant_id = $1`,
		tenantID,
	).Scan(&aplicadoHasta)
	if err != nil {
		return Frescura{}, fmt.Errorf("proyecciones: calcular frescura: %w", err)
	}
	if aplicadoHasta == nil {
		// Sin eventos aplicados para el tenant: no hay retraso que medir.
		return Frescura{}, nil
	}
	stale := l.techoFrescura > 0 && l.ahora().Sub(*aplicadoHasta) > l.techoFrescura
	return Frescura{AplicadoHasta: *aplicadoHasta, Stale: stale}, nil
}
