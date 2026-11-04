package proyecciones_test

import (
	"context"
	"io/fs"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-svc-reportes/internal/dbtest"
	"github.com/AI27-Tech/pf-svc-reportes/internal/proyecciones"
	"github.com/AI27-Tech/pf-svc-reportes/migrations"
	"github.com/jackc/pgx/v5"
)

// TestLector ejerce las dos lecturas contra Postgres real, sembrando las
// proyecciones directo (el lado escritura se prueba aparte en proyeccionpg).
// Afirma lo que el consumidor del reporte observa: las filas del periodo, el
// aislamiento por tenant (P9) y la marca de frescura (proyeccion_al + stale).
// Se salta si REPORTES_TEST_DB no está definido (CI sin Postgres).
func TestLector(t *testing.T) {
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
	applyMigrations(t, ctx, admin)

	const (
		tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		viajeA1 = "11111111-1111-1111-1111-111111111111"
		viajeA2 = "22222222-2222-2222-2222-222222222222"
		viajeB1 = "33333333-3333-3333-3333-333333333333"
		viajeB2 = "44444444-4444-4444-4444-444444444444"
	)
	ptr := func(s string) *string { return &s }
	// Instantes de dominio deterministas. El más reciente aplicado del tenant A
	// es t1 (creado del viaje A2); la frescura del reporte sale de ahí.
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	// t2 es el último evento global (viaje B2): más reciente que todo lo de A,
	// para distinguir la frescura global (Interno) de la de un solo tenant.
	t2 := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

	// Siembra con el admin: dos viajes del tenant A y dos del B (aislamiento).
	// aplicado_hasta = creado_en, igual que el lado escritura (adaptador.go).
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_viaje
		(viaje_id, tenant_id, cliente_id, estado_actual, creado_en, aplicado_hasta) VALUES
		('`+viajeA1+`','`+tenantA+`','`+tenantA+`','APROBADO','`+t0.Format(time.RFC3339)+`','`+t0.Format(time.RFC3339)+`'),
		('`+viajeA2+`','`+tenantA+`','`+tenantA+`','SOLICITADO','`+t1.Format(time.RFC3339)+`','`+t1.Format(time.RFC3339)+`'),
		('`+viajeB1+`','`+tenantB+`','`+tenantB+`','APROBADO','`+t0.Format(time.RFC3339)+`','`+t0.Format(time.RFC3339)+`'),
		('`+viajeB2+`','`+tenantB+`','`+tenantB+`','SOLICITADO','`+t2.Format(time.RFC3339)+`','`+t2.Format(time.RFC3339)+`')`)
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_transicion
		(viaje_id, tenant_id, de_estado, a_estado, ocurrido_en, evento_id) VALUES
		('`+viajeA1+`','`+tenantA+`','SOLICITADO','APROBADO','`+t1.Format(time.RFC3339)+`','`+viajeA1+`'),
		('`+viajeB1+`','`+tenantB+`','SOLICITADO','APROBADO','`+t0.Format(time.RFC3339)+`','`+viajeB1+`')`)

	raw, closeRaw := dbtest.PoolAs(t, ctx, "svc_reportes", "reportes")
	defer closeRaw()
	pool := &pgxpool.Pool{Pool: raw, Role: "svc_reportes"}

	periodo := func() (time.Time, time.Time) {
		return t0.Add(-time.Hour), t1.Add(time.Hour)
	}

	t.Run("viajes del tenant A, aislado de B, frescura no stale", func(t *testing.T) {
		// Techo amplio y reloj anclado poco después de t1: no stale.
		lector := proyecciones.NewLector(pool, time.Hour)
		proyecciones.ConRelojParaPrueba(lector, t1.Add(10*time.Minute))
		desde, hasta := periodo()

		filas, fr, err := lector.Viajes(ctx, ptr(tenantA), &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes: %v", err)
		}
		if len(filas) != 2 {
			t.Fatalf("filas = %d, se esperaban 2 (solo tenant A)", len(filas))
		}
		for _, f := range filas {
			if f.ClienteID == tenantB {
				t.Fatalf("fuga de tenant: apareció un viaje de B en el reporte de A")
			}
		}
		if !fr.AplicadoHasta.Equal(t1) {
			t.Fatalf("proyeccion_al = %v, se esperaba %v (MAX aplicado_hasta de A)", fr.AplicadoHasta, t1)
		}
		if fr.Stale {
			t.Fatalf("stale = true con reloj 10 min después del último evento y techo de 1h")
		}
	})

	t.Run("stale cuando el retraso excede el techo", func(t *testing.T) {
		// Techo corto y reloj muy posterior: stale.
		lector := proyecciones.NewLector(pool, time.Minute)
		proyecciones.ConRelojParaPrueba(lector, t1.Add(24*time.Hour))
		desde, hasta := periodo()

		_, fr, err := lector.Viajes(ctx, ptr(tenantA), &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes: %v", err)
		}
		if !fr.Stale {
			t.Fatalf("stale = false con reloj 24h después del último evento y techo de 1 min")
		}
	})

	t.Run("estados del tenant A, aislado de B", func(t *testing.T) {
		lector := proyecciones.NewLector(pool, time.Hour)
		desde, hasta := periodo()

		conteos, _, err := lector.Estados(ctx, ptr(tenantA), desde, hasta)
		if err != nil {
			t.Fatalf("Estados: %v", err)
		}
		// A tiene una sola transición (-> APROBADO); la de B no debe contarse.
		total := 0
		for _, c := range conteos {
			total += c.Conteo
		}
		if total != 1 {
			t.Fatalf("conteo total = %d, se esperaba 1 (solo la transición de A)", total)
		}
	})

	t.Run("tenant B: solo sus dos viajes y su frescura", func(t *testing.T) {
		lector := proyecciones.NewLector(pool, time.Hour)
		desde, hasta := periodo()

		filas, fr, err := lector.Viajes(ctx, ptr(tenantB), &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes: %v", err)
		}
		if got, want := ids(filas), []string{viajeB1, viajeB2}; !slices.Equal(got, want) {
			t.Fatalf("viajes = %v, se esperaban %v (solo tenant B)", got, want)
		}
		if !fr.AplicadoHasta.Equal(t2) {
			t.Fatalf("proyeccion_al = %v, se esperaba %v (MAX aplicado_hasta de B)", fr.AplicadoHasta, t2)
		}
	})

	// DEC-129: la audiencia Interno llega con tenant nil y ve a todos los clientes.
	t.Run("interno (tenant nil) ve viajes de A y B con frescura global", func(t *testing.T) {
		// Techo de 1h y reloj a t1+70min: la frescura de A sola (t1) sería
		// stale; la global (t2, 40 min antes) no. Así se ve que la frescura sale
		// del mismo conjunto global que las filas.
		lector := proyecciones.NewLector(pool, time.Hour)
		proyecciones.ConRelojParaPrueba(lector, t1.Add(70*time.Minute))
		desde, hasta := periodo()

		filas, fr, err := lector.Viajes(ctx, nil, &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes: %v", err)
		}
		if got, want := ids(filas), []string{viajeA1, viajeA2, viajeB1, viajeB2}; !slices.Equal(got, want) {
			t.Fatalf("viajes = %v, se esperaban %v (A y B)", got, want)
		}
		if !fr.AplicadoHasta.Equal(t2) {
			t.Fatalf("proyeccion_al = %v, se esperaba %v (MAX aplicado_hasta global)", fr.AplicadoHasta, t2)
		}
		if fr.Stale {
			t.Fatalf("stale = true: la frescura global es t2, 40 min antes del reloj, bajo el techo de 1h")
		}

		_, frA, err := lector.Viajes(ctx, ptr(tenantA), &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes A: %v", err)
		}
		if !frA.Stale {
			t.Fatalf("control: la frescura de A sola (t1, 70 min) debía ser stale con techo de 1h")
		}
	})

	t.Run("interno (tenant nil) cuenta estados de A y B", func(t *testing.T) {
		lector := proyecciones.NewLector(pool, time.Hour)
		desde, hasta := periodo()

		conteos, fr, err := lector.Estados(ctx, nil, desde, hasta)
		if err != nil {
			t.Fatalf("Estados: %v", err)
		}
		if len(conteos) != 1 || conteos[0].Estado != "APROBADO" || conteos[0].Conteo != 2 {
			t.Fatalf("conteos = %+v, se esperaba [{APROBADO 2}] (una transición de A y una de B)", conteos)
		}
		if !fr.AplicadoHasta.Equal(t2) {
			t.Fatalf("proyeccion_al = %v, se esperaba %v (global)", fr.AplicadoHasta, t2)
		}
	})

	t.Run("tenant sin datos: proyeccion_al cero, no stale", func(t *testing.T) {
		lector := proyecciones.NewLector(pool, time.Minute)
		desde, hasta := periodo()

		filas, fr, err := lector.Viajes(ctx, ptr("cccccccc-cccc-cccc-cccc-cccccccccccc"), &desde, &hasta, false)
		if err != nil {
			t.Fatalf("Viajes: %v", err)
		}
		if len(filas) != 0 {
			t.Fatalf("filas = %d, se esperaban 0 para un tenant sin datos", len(filas))
		}
		if !fr.AplicadoHasta.IsZero() {
			t.Fatalf("proyeccion_al = %v, se esperaba cero para un tenant sin eventos", fr.AplicadoHasta)
		}
		if fr.Stale {
			t.Fatalf("stale = true sin eventos aplicados: no hay retraso que medir")
		}
	})
}

// ids devuelve los viaje_id ordenados, para comparar conjuntos sin depender del
// orden entre viajes creados en el mismo instante.
func ids(filas []proyecciones.FilaViaje) []string {
	out := make([]string, 0, len(filas))
	for _, f := range filas {
		out = append(out, f.ViajeID)
	}
	sort.Strings(out)
	return out
}

func applyMigrations(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
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
		if _, err := admin.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("aplicar %s: %v", f, err)
		}
	}
}
