package reportes

import (
	"context"
	"testing"

	"github.com/AI27-Tech/pf-pkg-platform-go/auth"
	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
)

// TestTenantDe fija el alcance de lectura por audiencia (DEC-129): Interno lee
// todos los clientes (nil) aunque traiga el tenant de AI27; Cliente, y cualquier
// audiencia que no sea Interno, queda acotado a su tenant (DEC-129 #2, DEC-112).
func TestTenantDe(t *testing.T) {
	const (
		tenantAI27    = "00000000-0000-0000-0000-0000000000a1"
		tenantCliente = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	casos := []struct {
		nombre     string
		claims     *auth.Claims
		wantTenant string // "" = sin filtro (nil)
		wantNoAut  bool
	}{
		{"interno con tenant de AI27: sin filtro", &auth.Claims{Audiencia: auth.AudienciaInterno, Rol: "monitorista", TenantID: tenantAI27}, "", false},
		{"interno sin tenant: sin filtro", &auth.Claims{Audiencia: auth.AudienciaInterno, Rol: "administrador"}, "", false},
		{"cliente: acotado a su tenant", &auth.Claims{Audiencia: auth.AudienciaCliente, Rol: "cliente", TenantID: tenantCliente}, tenantCliente, false},
		{"audiencia vacía: acotado a su tenant", &auth.Claims{TenantID: tenantCliente}, tenantCliente, false},
		{"cliente sin tenant: no autorizado", &auth.Claims{Audiencia: auth.AudienciaCliente, Rol: "cliente"}, "", true},
		{"sin claims: no autorizado", nil, "", true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ctx := context.Background()
			if c.claims != nil {
				ctx = auth.ContextWithClaims(ctx, c.claims)
			}
			got, err := tenantDe(ctx)
			if c.wantNoAut {
				if platformerrors.AsCategory(err) != platformerrors.CategoryNoAutorizado {
					t.Fatalf("err = %v, se esperaba no autorizado", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			switch {
			case c.wantTenant == "" && got != nil:
				t.Fatalf("tenant = %q, se esperaba nil (todos los clientes)", *got)
			case c.wantTenant != "" && (got == nil || *got != c.wantTenant):
				t.Fatalf("tenant = %v, se esperaba %q", got, c.wantTenant)
			}
		})
	}
}
