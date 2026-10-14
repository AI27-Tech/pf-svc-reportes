package reportes

import (
	"context"
	"testing"

	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
)

// workerInerte implementa worker.Worker sin hacer nada; solo para comprobar que
// Workers() devuelve el worker que New() creó.
type workerInerte struct{}

func (workerInerte) Name() string                { return "inerte" }
func (workerInerte) Run(context.Context) error   { return nil }
func (workerInerte) Ready(context.Context) error { return nil }

// subscriberFake registra la suscripción pedida y devuelve un worker inerte, para
// comprobar el cableado de Workers() sin bus real.
type subscriberFake struct {
	durable   string
	superfice bus.Sensibilidad
	tipos     []string
}

func (s *subscriberFake) Subscribe(durableName string, superficie bus.Sensibilidad, filtroTipos []string, _ bus.Handler) (worker.Worker, error) {
	s.durable = durableName
	s.superfice = superficie
	s.tipos = filtroTipos
	return workerInerte{}, nil
}

// TestWorkersIncluyeConsumidorConSubscriber verifica que, cuando la raíz inyecta
// un Subscriber, New() crea el consumidor durable de proyecciones y Workers() lo
// devuelve, suscrito a la superficie amplia y a los 4 tipos viaje.* con productor.
func TestWorkersIncluyeConsumidorConSubscriber(t *testing.T) {
	sub := &subscriberFake{}
	m, err := New(Pools{}, Deps{Subscriber: sub})
	if err != nil {
		t.Fatalf("New con Subscriber: %v", err)
	}
	if got := m.Workers(); len(got) != 1 {
		t.Fatalf("Workers() devolvió %d workers; se esperaba 1 (el consumidor de proyecciones)", len(got))
	}
	if sub.durable != "reportes" {
		t.Fatalf("durableName = %q; se esperaba \"reportes\"", sub.durable)
	}
	if sub.superfice != bus.SensibilidadAmplia {
		t.Fatalf("superficie = %q; se esperaba amplia", sub.superfice)
	}
	if len(sub.tipos) != 4 {
		t.Fatalf("filtroTipos = %v; se esperaban los 4 tipos viaje.* con productor", sub.tipos)
	}
}

// TestWorkersVacioSinSubscriber verifica que sin bus (arranque standalone) no se
// abre ningún consumidor: Workers() va vacío.
func TestWorkersVacioSinSubscriber(t *testing.T) {
	m, err := New(Pools{}, Deps{})
	if err != nil {
		t.Fatalf("New sin Subscriber: %v", err)
	}
	if got := m.Workers(); len(got) != 0 {
		t.Fatalf("Workers() devolvió %d workers; se esperaba 0 sin Subscriber", len(got))
	}
}
