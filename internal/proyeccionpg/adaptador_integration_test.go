package proyeccionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"testing"
	"time"

	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-svc-reportes/internal/consumidores"
	"github.com/AI27-Tech/pf-svc-reportes/internal/dbtest"
	"github.com/AI27-Tech/pf-svc-reportes/internal/proyeccionpg"
	"github.com/AI27-Tech/pf-svc-reportes/migrations"
	"github.com/jackc/pgx/v5"
	ppgx "github.com/jackc/pgx/v5/pgxpool"
)

// TestProyeccion ejerce el adaptador real contra Postgres a través del mismo
// ConsumidorProyecciones que corre en producción: siembra sobres del bus, los
// decodifica y procesa, y afirma las filas resultantes en hecho_viaje y
// hecho_transicion. Cubre el DoD del Paso 1 (plan E6-T1):
//   - cada uno de los 4 eventos puebla/actualiza su fila;
//   - un duplicado (mismo evento_id) no duplica efecto;
//   - dos tenants (dos cliente_id) no se mezclan;
//   - un estado_cambiado sin creado previo va al borde (ErrTenantDesconocido).
//
// Se salta si REPORTES_TEST_DB no está definido (CI sin Postgres).
func TestProyeccion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbtest.Serializar(t, ctx) // no correr a la vez que el grants test (misma base)

	admin, closeAdmin := dbtest.AdminConn(t, ctx)
	defer closeAdmin()

	cleanup := func() {
		dbtest.Exec(t, ctx, admin, "DROP SCHEMA IF EXISTS reportes CASCADE")
		dbtest.Exec(t, ctx, admin, "DROP ROLE IF EXISTS svc_reportes")
	}
	cleanup()
	defer cleanup()
	applyMigrations(t, ctx, admin)

	raw, closeRaw := dbtest.PoolAs(t, ctx, "svc_reportes", "reportes")
	defer closeRaw()
	pool := &pgxpool.Pool{Pool: raw, Role: "svc_reportes"}

	ad := proyeccionpg.New(pool, nil)
	c := consumidores.NewConsumidorProyecciones(ad, ad, ad)

	const (
		viajeA  = "11111111-1111-1111-1111-111111111111"
		viajeB  = "22222222-2222-2222-2222-222222222222"
		tenantA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tenantB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		unidad1 = "99999999-9999-9999-9999-999999999991"
		unidad2 = "99999999-9999-9999-9999-999999999992"

		evCreadoA = "e0000000-0000-0000-0000-0000000000a1"
		evCreadoB = "e0000000-0000-0000-0000-0000000000b1"
		evEstadoA = "e0000000-0000-0000-0000-0000000000a2"
		evAsigA   = "e0000000-0000-0000-0000-0000000000a3"
		evReasigA = "e0000000-0000-0000-0000-0000000000a4"
		evHuerfo  = "e0000000-0000-0000-0000-00000000dead"
		viajeC    = "33333333-3333-3333-3333-333333333333"
	)
	ahora := time.Now().UTC().Truncate(time.Second)

	// --- viaje.creado de dos tenants distintos ---
	procesar(t, ctx, c, evCreadoA, "viaje.creado", viajeA, map[string]any{
		"viaje_id": viajeA, "cliente_id": tenantA, "estado": "SOLICITADO", "creado_en": ahora,
	})
	procesar(t, ctx, c, evCreadoB, "viaje.creado", viajeB, map[string]any{
		"viaje_id": viajeB, "cliente_id": tenantB, "estado": "APROBADO", "creado_en": ahora,
	})

	// tenant = cliente_id, y no se mezclan.
	if got := scanTenant(t, ctx, raw, viajeA); got != tenantA {
		t.Fatalf("hecho_viaje[A].tenant_id = %q; se esperaba %q", got, tenantA)
	}
	if got := scanTenant(t, ctx, raw, viajeB); got != tenantB {
		t.Fatalf("hecho_viaje[B].tenant_id = %q; se esperaba %q", got, tenantB)
	}

	// --- viaje.estado_cambiado: actualiza estado e inserta la transición ---
	procesar(t, ctx, c, evEstadoA, "viaje.estado_cambiado", viajeA, map[string]any{
		"viaje_id": viajeA, "de_estado": "SOLICITADO", "a_estado": "APROBADO", "cambiado_en": ahora,
	})
	if got := scanEstado(t, ctx, raw, viajeA); got != "APROBADO" {
		t.Fatalf("hecho_viaje[A].estado_actual = %q; se esperaba APROBADO", got)
	}
	tv, te := scanTransicion(t, ctx, raw, viajeA)
	if tv != tenantA || te != "APROBADO" {
		t.Fatalf("hecho_transicion[A] = (tenant %q, a_estado %q); se esperaba (%q, APROBADO)", tv, te, tenantA)
	}

	// --- viaje.asignado: vuelca la escolta a unidades_ids ---
	procesar(t, ctx, c, evAsigA, "viaje.asignado", viajeA, map[string]any{
		"viaje_id": viajeA, "unidades_ids": []string{unidad1, unidad2}, "asignada_en": ahora,
	})
	if got := scanUnidades(t, ctx, raw, viajeA); len(got) != 2 || got[0] != unidad1 || got[1] != unidad2 {
		t.Fatalf("hecho_viaje[A].unidades_ids = %v; se esperaba [%s %s]", got, unidad1, unidad2)
	}

	// --- viaje.reasignado: reemplaza la escolta completa ---
	procesar(t, ctx, c, evReasigA, "viaje.reasignado", viajeA, map[string]any{
		"viaje_id": viajeA, "unidades_ids": []string{unidad2}, "motivo": "cambio", "reasignado_en": ahora,
	})
	if got := scanUnidades(t, ctx, raw, viajeA); len(got) != 1 || got[0] != unidad2 {
		t.Fatalf("tras reasignar, unidades_ids = %v; se esperaba [%s]", got, unidad2)
	}

	// --- idempotencia: reprocesar el estado_cambiado no duplica la transición ---
	procesar(t, ctx, c, evEstadoA, "viaje.estado_cambiado", viajeA, map[string]any{
		"viaje_id": viajeA, "de_estado": "SOLICITADO", "a_estado": "APROBADO", "cambiado_en": ahora,
	})
	if n := contarTransiciones(t, ctx, raw, viajeA); n != 1 {
		t.Fatalf("hecho_transicion[A] tiene %d filas tras reprocesar; se esperaba 1 (idempotencia)", n)
	}

	// --- borde: estado_cambiado de un viaje sin creado previo ---
	err := ad.Aplicar(ctx, consumidores.Evento{
		EventoID: evHuerfo, Tipo: "viaje.estado_cambiado", AgregadoID: viajeC,
		Payload: payloadRaw(t, map[string]any{
			"viaje_id": viajeC, "a_estado": "APROBADO", "cambiado_en": ahora,
		}),
	})
	if !errors.Is(err, proyeccionpg.ErrTenantDesconocido) {
		t.Fatalf("estado_cambiado huérfano: err = %v; se esperaba ErrTenantDesconocido", err)
	}
}

// procesar decodifica un sobre y lo corre por el consumidor, como en producción.
func procesar(t *testing.T, ctx context.Context, c *consumidores.ConsumidorProyecciones, eventoID, tipo, agregado string, payload map[string]any) {
	t.Helper()
	s := bus.Sobre{EventoID: eventoID, Tipo: tipo, Version: 1, AgregadoID: agregado, Payload: mustJSON(t, payload)}
	if err := c.Procesar(ctx, proyeccionpg.Decodificar(s)); err != nil {
		t.Fatalf("procesar %s (%s): %v", tipo, eventoID, err)
	}
}

func payloadRaw(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	return json.RawMessage(mustJSON(t, m))
}

func mustJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func scanTenant(t *testing.T, ctx context.Context, pool *ppgx.Pool, viaje string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(ctx, `SELECT tenant_id FROM reportes.hecho_viaje WHERE viaje_id=$1`, viaje).Scan(&v); err != nil {
		t.Fatalf("leer tenant de %s: %v", viaje, err)
	}
	return v
}

func scanEstado(t *testing.T, ctx context.Context, pool *ppgx.Pool, viaje string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(ctx, `SELECT estado_actual FROM reportes.hecho_viaje WHERE viaje_id=$1`, viaje).Scan(&v); err != nil {
		t.Fatalf("leer estado de %s: %v", viaje, err)
	}
	return v
}

func scanUnidades(t *testing.T, ctx context.Context, pool *ppgx.Pool, viaje string) []string {
	t.Helper()
	var v []string
	if err := pool.QueryRow(ctx, `SELECT unidades_ids FROM reportes.hecho_viaje WHERE viaje_id=$1`, viaje).Scan(&v); err != nil {
		t.Fatalf("leer unidades de %s: %v", viaje, err)
	}
	return v
}

func scanTransicion(t *testing.T, ctx context.Context, pool *ppgx.Pool, viaje string) (tenant, aEstado string) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT tenant_id, a_estado FROM reportes.hecho_transicion WHERE viaje_id=$1`, viaje).Scan(&tenant, &aEstado); err != nil {
		t.Fatalf("leer transición de %s: %v", viaje, err)
	}
	return tenant, aEstado
}

func contarTransiciones(t *testing.T, ctx context.Context, pool *ppgx.Pool, viaje string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reportes.hecho_transicion WHERE viaje_id=$1`, viaje).Scan(&n); err != nil {
		t.Fatalf("contar transiciones de %s: %v", viaje, err)
	}
	return n
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
