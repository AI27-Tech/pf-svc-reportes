// Package presupuesto es la PUERTA ÚNICA de ejecución de consultas de reportes
// (reportes.md (d), (j)). Toda consulta pasa por aquí: límite de tiempo y de
// memoria por consulta, cancelación y atribución de costo por reporte. Es lo que
// hace defendible el eje de escalamiento de memoria de reportes (reportes.md
// (d), (f)); el llamador nunca envía SQL ni filtros arbitrarios.
//
// Los límites son CONFIGURABLES; ningún valor por defecto se presenta como
// decidido (reportes.md (j), ADR 29 §4). El scaffold fija valores de arranque
// explícitamente marcados como no aprobados.
package presupuesto

import (
	"context"
	"time"

	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
)

// Limites define el presupuesto por consulta. Configurable por despliegue; los
// valores de abajo son de arranque y NO están aprobados como criterio Pass/Fail.
type Limites struct {
	// TiempoMaximo acota la duración de una consulta antes de cancelarla.
	TiempoMaximo time.Duration
	// MemoriaMaximaBytes es el techo de memoria atribuible a una consulta. El
	// scaffold lo registra y lo expone; la medición real se cablea con el motor
	// de consulta (TODO).
	MemoriaMaximaBytes int64
}

// LimitesPorDefecto son valores de arranque, explícitamente NO aprobados
// (reportes.md (j)). Existen para que la puerta funcione en desarrollo, no como
// decisión de producto.
var LimitesPorDefecto = Limites{
	TiempoMaximo:       5 * time.Second,
	MemoriaMaximaBytes: 128 << 20, // 128 MiB, provisional
}

// Puerta envuelve toda ejecución de consulta con el presupuesto. Zero value no
// usable: construir con Nueva.
type Puerta struct {
	limites Limites
}

// Nueva crea la puerta con los límites dados.
func Nueva(l Limites) *Puerta {
	return &Puerta{limites: l}
}

// Ejecutar corre fn bajo el presupuesto de tiempo, propagando cancelación. La
// atribución de costo por reporte (reporte es la etiqueta) se registra para
// métricas; la medición de memoria se cablea con el motor de consulta (TODO).
//
// Si el presupuesto de tiempo se agota, devuelve un error Degradado con la
// taxonomía de la plataforma (I3), no un error opaco.
func (p *Puerta) Ejecutar(ctx context.Context, reporte string, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, p.limites.TiempoMaximo)
	defer cancel()

	// TODO(reportes): atribución de costo (reporte) a métricas y medición de
	// memoria por consulta contra MemoriaMaximaBytes cuando exista el motor de
	// consulta. Hoy la puerta impone tiempo y cancelación, que es lo verificable.
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return platformerrors.NewDegradadoErr(ctx.Err(), "reportes: consulta %q excedió el presupuesto de tiempo", reporte)
	}
}
