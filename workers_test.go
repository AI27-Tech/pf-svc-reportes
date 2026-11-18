package reportes

import (
	"context"
	"testing"

	idclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/identidad"
	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
)

// workerInerte implementa worker.Worker sin hacer nada; solo para comprobar que
// Workers() devuelve el worker que New() creó.
type workerInerte struct{ nombre string }

func (w workerInerte) Name() string              { return w.nombre }
func (workerInerte) Run(context.Context) error   { return nil }
func (workerInerte) Ready(context.Context) error { return nil }

// suscripcion es una llamada a Subscribe registrada por subscriberFake.
type suscripcion struct {
	superficie bus.Sensibilidad
	tipos      []string
	h          bus.Handler
}

// subscriberFake registra cada suscripción pedida, por durable, y devuelve un
// worker inerte, para comprobar el cableado de Workers() sin bus real.
type subscriberFake struct {
	subs map[string]suscripcion
}

func (s *subscriberFake) Subscribe(durableName string, superficie bus.Sensibilidad, filtroTipos []string, h bus.Handler) (worker.Worker, error) {
	if s.subs == nil {
		s.subs = map[string]suscripcion{}
	}
	s.subs[durableName] = suscripcion{superficie, filtroTipos, h}
	return workerInerte{nombre: "bus-durable:" + durableName}, nil
}

// alcancesFake es AlcancesPort con páginas fijas: la primera sin cursor, la
// segunda con él.
type alcancesFake struct {
	snapshot, continuacion *idclient.PaginaAlcances
}

func (a *alcancesFake) ListarAlcances(_ context.Context, p idclient.ListarAlcancesParams) (*idclient.PaginaAlcances, error) {
	if p.Cursor == nil {
		return a.snapshot, nil
	}
	return a.continuacion, nil
}

// TestWorkersIncluyeConsumidorConSubscriber verifica que, cuando la raíz inyecta
// un Subscriber, New() crea los dos consumidores durables y Workers() los
// devuelve: el de proyecciones, a la superficie amplia y a los 4 tipos viaje.*
// con productor, y el de alcance.cambiado (DEC-149 #20) con su propio durable.
func TestWorkersIncluyeConsumidorConSubscriber(t *testing.T) {
	sub := &subscriberFake{}
	m, err := New(Pools{}, Deps{Subscriber: sub})
	if err != nil {
		t.Fatalf("New con Subscriber: %v", err)
	}
	nombres := map[string]bool{}
	for _, w := range m.Workers() {
		nombres[w.Name()] = true
	}
	if len(nombres) != 2 || !nombres["bus-durable:reportes"] || !nombres["bus-durable:reportes-alcance"] {
		t.Fatalf("Workers() = %v; se esperaban el consumidor de proyecciones y el de alcance", nombres)
	}
	p, ok := sub.subs["reportes"]
	if !ok || p.superficie != bus.SensibilidadAmplia || len(p.tipos) != 4 {
		t.Fatalf("suscripción reportes = %+v; se esperaba amplia con los 4 tipos viaje.* con productor", p)
	}
	a, ok := sub.subs["reportes-alcance"]
	if !ok || a.superficie != bus.SensibilidadAmplia || len(a.tipos) != 1 || a.tipos[0] != "alcance.cambiado" {
		t.Fatalf("suscripción reportes-alcance = %+v; se esperaba amplia a alcance.cambiado", a)
	}
}

// TestWorkersConAlcancesArrancaNotReady fija que, con Deps.Alcances, el
// consumidor de alcance va envuelto por la reconstrucción y arranca not-ready
// hasta converger (ADR 26 §4).
func TestWorkersConAlcancesArrancaNotReady(t *testing.T) {
	m, err := New(Pools{}, Deps{Subscriber: &subscriberFake{}, Alcances: &alcancesFake{}})
	if err != nil {
		t.Fatalf("New con Alcances: %v", err)
	}
	for _, w := range m.Workers() {
		if w.Name() == "reportes-alcance" {
			if w.Ready(context.Background()) == nil {
				t.Error("reconstrucción sin correr: Ready nil; se esperaba not-ready")
			}
			return
		}
	}
	t.Error("con Alcances: falta el worker reportes-alcance")
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
