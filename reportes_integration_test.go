package reportes

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"
	"time"

	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/auth"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-svc-reportes/internal/dbtest"
	"github.com/AI27-Tech/pf-svc-reportes/migrations"
)

// TestReportesInternoVeTodos fija que la audiencia Interno conserva DEC-129 por
// HTTP y por la fachada: con el tenant de AI27 ve los viajes de los dos clientes
// sembrados. Y que el cliente recibe 403 aunque haya datos de su propia empresa.
// Se salta si REPORTES_TEST_DB no está definido (CI sin Postgres).
func TestReportesInternoVeTodos(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbtest.Serializar(t, ctx)
	admin, closeAdmin := dbtest.AdminConn(t, ctx)
	defer closeAdmin()
	cleanup := func() {
		dbtest.Exec(t, ctx, admin, "DROP SCHEMA IF EXISTS reportes CASCADE")
		dbtest.Exec(t, ctx, admin, "DROP ROLE IF EXISTS svc_reportes")
	}
	cleanup()
	defer cleanup()
	files, err := fs.Glob(migrations.FS, "reportes/*.sql")
	if err != nil {
		t.Fatalf("listar migraciones: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := migrations.FS.ReadFile(f)
		if err != nil {
			t.Fatalf("leer %s: %v", f, err)
		}
		dbtest.Exec(t, ctx, admin, string(sql))
	}

	// Dos viajes del cliente A y uno del B; una transición a APROBADO por cliente.
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_viaje
		(viaje_id, tenant_id, cliente_id, estado_actual, creado_en, aplicado_hasta) VALUES
		('11111111-1111-1111-1111-111111111111','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','APROBADO','2026-10-01T08:00:00Z','2026-10-01T08:00:00Z'),
		('22222222-2222-2222-2222-222222222222','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','SOLICITADO','2026-10-01T09:00:00Z','2026-10-01T09:00:00Z'),
		('33333333-3333-3333-3333-333333333333','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','APROBADO','2026-10-01T09:30:00Z','2026-10-01T09:30:00Z')`)
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_transicion
		(viaje_id, tenant_id, de_estado, a_estado, ocurrido_en, evento_id) VALUES
		('11111111-1111-1111-1111-111111111111','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','SOLICITADO','APROBADO','2026-10-01T09:00:00Z','11111111-1111-1111-1111-111111111111'),
		('33333333-3333-3333-3333-333333333333','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','SOLICITADO','APROBADO','2026-10-01T09:40:00Z','33333333-3333-3333-3333-333333333333')`)

	raw, closeRaw := dbtest.PoolAs(t, ctx, "svc_reportes", "reportes")
	defer closeRaw()
	m, err := New(Pools{Reportes: &pgxpool.Pool{Pool: raw, Role: "svc_reportes"}}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	m.Routes(mux)

	interno := auth.ContextWithClaims(ctx, &auth.Claims{Audiencia: auth.AudienciaInterno, Rol: "monitorista", TenantID: "00000000-0000-0000-0000-0000000000a1"})
	cliente := auth.ContextWithClaims(ctx, claimsCliente) // tenant A, que sí tiene viajes
	wantViajes := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
	}
	wantAl := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC) // MAX aplicado_hasta global
	desde := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hasta := desde.Add(24 * time.Hour)

	get := func(t *testing.T, ctx context.Context, ruta string, v any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ruta+periodoQuery, nil).WithContext(ctx))
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decodificar %s: %v (%s)", ruta, err, rec.Body.String())
		}
		return rec.Code
	}
	checkViajes := func(t *testing.T, rep *repclient.ReporteViajes) {
		t.Helper()
		var got []string
		for _, f := range rep.Filas {
			got = append(got, f.ViajeId.String())
		}
		sort.Strings(got)
		if rep.Total != 3 || !slices.Equal(got, wantViajes) {
			t.Fatalf("total = %d, viajes = %v; se esperaban 3: %v", rep.Total, got, wantViajes)
		}
		if rep.ProyeccionAl == nil || !rep.ProyeccionAl.Equal(wantAl) || rep.Stale == nil || *rep.Stale {
			t.Fatalf("frescura = (%v, %v), se esperaba (%v, false)", rep.ProyeccionAl, rep.Stale, wantAl)
		}
	}
	checkEstados := func(t *testing.T, rep *repclient.ReporteEstados) {
		t.Helper()
		if len(rep.Conteos) != 1 || rep.Conteos[0].Estado != "APROBADO" || rep.Conteos[0].Conteo != 2 {
			t.Fatalf("conteos = %+v, se esperaba [{APROBADO 2}]", rep.Conteos)
		}
	}

	t.Run("interno HTTP viajes: 200 con los 3 viajes de A y B", func(t *testing.T) {
		var rep repclient.ReporteViajes
		if code := get(t, interno, "/api/v1/reportes/viajes", &rep); code != 200 {
			t.Fatalf("status = %d, se esperaba 200", code)
		}
		checkViajes(t, &rep)
	})
	t.Run("interno HTTP estados: 200 con APROBADO 2", func(t *testing.T) {
		var rep repclient.ReporteEstados
		if code := get(t, interno, "/api/v1/reportes/estados", &rep); code != 200 {
			t.Fatalf("status = %d, se esperaba 200", code)
		}
		checkEstados(t, &rep)
	})
	t.Run("interno fachada: mismos resultados", func(t *testing.T) {
		rv, err := m.Consultas().ReporteViajes(interno, repclient.ReporteViajesParams{Desde: desde, Hasta: hasta})
		if err != nil {
			t.Fatalf("ReporteViajes: %v", err)
		}
		checkViajes(t, rv)
		re, err := m.Consultas().ReporteEstados(interno, repclient.ReporteEstadosParams{Desde: desde, Hasta: hasta})
		if err != nil {
			t.Fatalf("ReporteEstados: %v", err)
		}
		checkEstados(t, re)
	})
	t.Run("cliente con datos propios: 403 en las dos rutas", func(t *testing.T) {
		for _, ruta := range []string{"/api/v1/reportes/viajes", "/api/v1/reportes/estados"} {
			var body repclient.Error
			if code := get(t, cliente, ruta, &body); code != 403 || body.Codigo != "prohibido" {
				t.Fatalf("%s = (%d, %q), se esperaba (403, \"prohibido\")", ruta, code, body.Codigo)
			}
		}
	})
}
