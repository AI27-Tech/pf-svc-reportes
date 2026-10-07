package presupuesto

import (
	"context"
	"testing"
	"time"

	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
)

// TestEjecutarRespetaCamino verifica que una consulta rápida devuelve su propio
// resultado sin tocar el presupuesto.
func TestEjecutarRespetaCamino(t *testing.T) {
	p := Nueva(Limites{TiempoMaximo: time.Second})
	err := p.Ejecutar(context.Background(), "reporteViajes", func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("no se esperaba error: %v", err)
	}
}

// TestEjecutarAgotaPresupuesto verifica que agotar el tiempo produce un error
// Degradado con la taxonomía de la plataforma (I3), no un error opaco.
func TestEjecutarAgotaPresupuesto(t *testing.T) {
	p := Nueva(Limites{TiempoMaximo: 20 * time.Millisecond})
	err := p.Ejecutar(context.Background(), "reporteEstados", func(ctx context.Context) error {
		select {
		case <-time.After(time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err == nil {
		t.Fatal("se esperaba error por presupuesto agotado")
	}
	if got := platformerrors.AsCategory(err); got != platformerrors.CategoryDegradado {
		t.Fatalf("categoría = %v; se esperaba Degradado", got)
	}
}
