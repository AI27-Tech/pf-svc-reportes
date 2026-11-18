package alcance

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPara fija la tabla rol × dashboard de DEC-149 #8 y reportes.yaml de
// tracer.42: administrador todo; monitoreo por clientes; cualquier otro, 403.
func TestPara(t *testing.T) {
	quiere := map[string]Ambito{
		"administrador":  Todos,
		"monitorista":    PorClientes,
		"supervisor":     PorClientes,
		"coordinador":    PorClientes,
		"jefe_operativo": Prohibido, // DEC-149 #8: el dashboard de monitoreo no es suyo
		"ejecutivo":      Prohibido,
		"reaccion":       Prohibido,
		"cliente":        Prohibido,
		"operador":       Prohibido, // rol retirado
		"":               Prohibido,
	}
	for rol, q := range quiere {
		if got := Para(rol); got != q {
			t.Errorf("Para(%q) = %d; quiere %d", rol, got, q)
		}
	}
}

// TestLeerPayload fija el parseo de alcance.cambiado tal como lo publica
// identidad; lo ilegible se descarta.
func TestLeerPayload(t *testing.T) {
	c := uuid.New()
	a, ok := leer([]byte(`{"sujeto_id":"11111111-1111-1111-1111-111111111111","idp_subject":"s-1","clientes_efectivos_ids":["` + c.String() + `"],"cambiado_en":"2026-11-17T10:00:00.123456Z"}`))
	if !ok || a.IdpSubject != "s-1" || len(a.Clientes) != 1 || a.Clientes[0] != c ||
		!a.CambiadoEn.Equal(time.Date(2026, 11, 17, 10, 0, 0, 123456000, time.UTC)) {
		t.Errorf("payload válido: %+v ok=%v", a, ok)
	}
	if a, ok := leer([]byte(`{"idp_subject":"s-1","clientes_efectivos_ids":[],"cambiado_en":"2026-11-17T10:00:00Z"}`)); !ok || a.Clientes == nil || len(a.Clientes) != 0 {
		t.Errorf("vacío = quitar: %+v ok=%v; quiere lista vacía", a, ok)
	}
	for nombre, malo := range map[string]string{
		"no es JSON": `no json`,
		// Un alcance de tracer.41 sin idp_subject no se puede indexar (DEC-149 #22).
		"sin idp_subject":     `{"sujeto_id":"11111111-1111-1111-1111-111111111111","clientes_efectivos_ids":[],"cambiado_en":"2026-11-17T10:00:00Z"}`,
		"idp_subject vacío":   `{"idp_subject":"","clientes_efectivos_ids":[],"cambiado_en":"2026-11-17T10:00:00Z"}`,
		"sin cambiado_en":     `{"idp_subject":"s-1","clientes_efectivos_ids":[]}`,
		"cliente que no UUID": `{"idp_subject":"s-1","clientes_efectivos_ids":["x"],"cambiado_en":"2026-11-17T10:00:00Z"}`,
	} {
		if _, ok := leer([]byte(malo)); ok {
			t.Errorf("%s: payload ilegible aceptado: %s", nombre, malo)
		}
	}
}
