package reportes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/auth"
	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
)

const periodoQuery = "?desde=2026-10-01T00:00:00Z&hasta=2026-10-02T00:00:00Z"

var (
	claimsCliente = &auth.Claims{Audiencia: auth.AudienciaCliente, Rol: "cliente", TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
	// Audiencia vacía o desconocida: no es Interno, así que tampoco lee.
	claimsSinAudiencia = &auth.Claims{Rol: "cliente", TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
)

// TestReportesRechazaNoInterno fija que solo la audiencia Interno lee reportes
// (DEC-022 #9): el cliente recibe 403 `prohibido` en las dos rutas y en la
// fachada en proceso, y sin claims es 401. El rechazo ocurre antes de tocar la
// base, por eso corre sin Postgres (el módulo no tiene pool).
func TestReportesRechazaNoInterno(t *testing.T) {
	m, err := New(Pools{}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	m.Routes(mux)
	q := m.Consultas()

	casos := []struct {
		nombre     string
		claims     *auth.Claims
		wantStatus int
		wantCodigo string
		wantCat    platformerrors.Category
	}{
		{"cliente: 403", claimsCliente, 403, "prohibido", platformerrors.CategoryProhibido},
		{"audiencia vacía: 403", claimsSinAudiencia, 403, "prohibido", platformerrors.CategoryProhibido},
		{"sin claims: 401", nil, 401, "no_autorizado", platformerrors.CategoryNoAutorizado},
	}
	for _, c := range casos {
		ctx := context.Background()
		if c.claims != nil {
			ctx = auth.ContextWithClaims(ctx, c.claims)
		}
		for _, ruta := range []string{"/api/v1/reportes/viajes", "/api/v1/reportes/estados"} {
			t.Run(c.nombre+" "+ruta, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, ruta+periodoQuery, nil).WithContext(ctx)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != c.wantStatus {
					t.Fatalf("status = %d, se esperaba %d", rec.Code, c.wantStatus)
				}
				var body repclient.Error
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("cuerpo no es Error: %v (%s)", err, rec.Body.String())
				}
				if body.Codigo != c.wantCodigo {
					t.Fatalf("codigo = %q, se esperaba %q", body.Codigo, c.wantCodigo)
				}
			})
		}
		t.Run(c.nombre+" fachada", func(t *testing.T) {
			desde := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			hasta := desde.Add(24 * time.Hour)
			rv, err := q.ReporteViajes(ctx, repclient.ReporteViajesParams{Desde: &desde, Hasta: &hasta})
			if rv != nil || platformerrors.AsCategory(err) != c.wantCat {
				t.Fatalf("ReporteViajes = (%v, %v), se esperaba (nil, %v)", rv, err, c.wantCat)
			}
			re, err := q.ReporteEstados(ctx, repclient.ReporteEstadosParams{Desde: desde, Hasta: hasta})
			if re != nil || platformerrors.AsCategory(err) != c.wantCat {
				t.Fatalf("ReporteEstados = (%v, %v), se esperaba (nil, %v)", re, err, c.wantCat)
			}
		})
	}
}

// TestReporteViajesValidaPeriodoYEnCurso fija la regla de reporteViajes: sin
// en_curso, desde y hasta son obligatorios; en_curso y las fechas deben tener
// formato válido; un rango invertido se rechaza siempre. Todo es 400 `validacion`
// por HTTP y por la fachada en proceso, y ocurre antes de tocar la base, por eso
// corre sin Postgres.
func TestReporteViajesValidaPeriodoYEnCurso(t *testing.T) {
	m, err := New(Pools{}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	m.Routes(mux)
	interno := auth.ContextWithClaims(context.Background(), &auth.Claims{Audiencia: auth.AudienciaInterno, Rol: "monitorista"})

	invalidos := map[string]string{
		"sin nada":                       "",
		"en_curso=false sin rango":       "?en_curso=false",
		"solo desde":                     "?desde=2026-10-01T00:00:00Z",
		"solo hasta":                     "?hasta=2026-10-02T00:00:00Z",
		"rango invertido":                "?desde=2026-10-02T00:00:00Z&hasta=2026-10-01T00:00:00Z",
		"rango invertido con en_curso":   "?desde=2026-10-02T00:00:00Z&hasta=2026-10-01T00:00:00Z&en_curso=true",
		"desde con formato inválido":     "?desde=ayer&hasta=2026-10-02T00:00:00Z",
		"hasta con formato inválido":     "?desde=2026-10-01T00:00:00Z&hasta=2026-10-02",
		"en_curso con formato inválido":  "?en_curso=quizas",
		"en_curso inválido con rango ok": periodoQuery + "&en_curso=quizas",
	}
	for nombre, query := range invalidos {
		t.Run(nombre, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/reportes/viajes"+query, nil).WithContext(interno)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, se esperaba 400; cuerpo: %s", rec.Code, rec.Body.String())
			}
			var body repclient.Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Codigo != "validacion" {
				t.Fatalf("cuerpo = %s, se esperaba codigo validacion", rec.Body.String())
			}
		})
	}

	t.Run("fachada sin en_curso ni rango: validación", func(t *testing.T) {
		rv, err := m.Consultas().ReporteViajes(interno, repclient.ReporteViajesParams{})
		if rv != nil || platformerrors.AsCategory(err) != platformerrors.CategoryValidacion {
			t.Fatalf("ReporteViajes = (%v, %v), se esperaba (nil, validación)", rv, err)
		}
	})
}
