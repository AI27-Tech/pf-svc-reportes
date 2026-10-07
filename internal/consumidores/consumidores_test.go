package consumidores

import (
	"context"
	"errors"
	"testing"
)

// proyeccionFake cuenta cuántas veces se aplicó cada evento, para detectar una
// doble aplicación (que, para viaje.estado_cambiado, duplicaría una duración).
type proyeccionFake struct {
	aplicados map[string]int
}

func (p *proyeccionFake) Aplicar(_ context.Context, e Evento) error {
	if p.aplicados == nil {
		p.aplicados = map[string]int{}
	}
	p.aplicados[e.EventoID]++
	return nil
}

// inboxMemoria modela inbox_evento en memoria.
type inboxMemoria struct {
	vistos map[string]struct{}
}

func (i *inboxMemoria) YaProcesado(_ context.Context, id string) (bool, error) {
	_, ok := i.vistos[id]
	return ok, nil
}

func (i *inboxMemoria) Marcar(_ context.Context, id, _, _ string) error {
	if i.vistos == nil {
		i.vistos = map[string]struct{}{}
	}
	i.vistos[id] = struct{}{}
	return nil
}

// dlqMemoria captura lo que entra en cuarentena.
type dlqMemoria struct {
	enCuarentena []string
}

func (d *dlqMemoria) Cuarentena(_ context.Context, id, _, _ string) error {
	d.enCuarentena = append(d.enCuarentena, id)
	return nil
}

// TestReaplicarNoDuplica verifica que reentregar el MISMO viaje.estado_cambiado
// (mismo evento_id) no aplica la proyección dos veces: la idempotencia por
// evento_id impide duplicar una duración (reportes.md (g), DoD).
func TestReaplicarNoDuplica(t *testing.T) {
	proy := &proyeccionFake{}
	c := NewConsumidorProyecciones(proy, &inboxMemoria{}, &dlqMemoria{})

	e := Evento{EventoID: "evt-1", Tipo: "viaje.estado_cambiado", AgregadoID: "viaje-1"}
	if err := c.Procesar(context.Background(), e); err != nil {
		t.Fatalf("primer procesar: %v", err)
	}
	if err := c.Procesar(context.Background(), e); err != nil {
		t.Fatalf("segundo procesar (duplicado): %v", err)
	}
	if n := proy.aplicados["evt-1"]; n != 1 {
		t.Fatalf("la proyección se aplicó %d veces; se esperaba 1 (idempotencia por evento_id)", n)
	}
}

// TestEventoVenenosoVaADLQ verifica que un evento de tipo no registrado va a la
// DLQ con su evento_id y no toca la proyección (ADR 29 §4, DoD).
func TestEventoVenenosoVaADLQ(t *testing.T) {
	proy := &proyeccionFake{}
	dlq := &dlqMemoria{}
	c := NewConsumidorProyecciones(proy, &inboxMemoria{}, dlq)

	e := Evento{EventoID: "evt-x", Tipo: "evento.desconocido", AgregadoID: "n/a"}
	err := c.Procesar(context.Background(), e)
	if !errors.Is(err, ErrEventoVenenoso) {
		t.Fatalf("err = %v; se esperaba ErrEventoVenenoso", err)
	}
	if len(dlq.enCuarentena) != 1 || dlq.enCuarentena[0] != "evt-x" {
		t.Fatalf("cuarentena = %v; se esperaba [evt-x]", dlq.enCuarentena)
	}
	if len(proy.aplicados) != 0 {
		t.Fatal("un evento venenoso no debe tocar la proyección")
	}
}
