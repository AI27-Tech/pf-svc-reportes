package proyecciones

import (
	"slices"
	"testing"
)

// CANCELADO_POR_CLIENTE es terminal (ADR 38): un viaje cancelado deja de contar
// como «en curso». Los estados retirados en tracer.45 ya no existen.
func TestEstadosEnCurso(t *testing.T) {
	for _, e := range []string{"CERRADO", "CANCELADO_POR_CLIENTE"} {
		if slices.Contains(estadosEnCurso, e) {
			t.Errorf("%s no debe contar como en curso", e)
		}
	}
	for _, e := range []string{"APROBADO", "ASIGNADO", "EN_TRANSITO"} {
		if !slices.Contains(estadosEnCurso, e) {
			t.Errorf("%s debe contar como en curso", e)
		}
	}
	for _, e := range []string{"SOLICITADO", "RECHAZADO_POR_AI27", "RECHAZADO_POR_CLIENTE", "CANCELADO"} {
		if slices.Contains(estadosEnCurso, e) {
			t.Errorf("estado retirado %s no debe aparecer", e)
		}
	}
}
