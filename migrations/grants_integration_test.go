package migrations_test

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AI27-Tech/pf-svc-reportes/internal/dbtest"
	"github.com/AI27-Tech/pf-svc-reportes/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	schema = "reportes"
	role   = "svc_reportes"
)

// TestGrants verifica el invariante I1 (ADR 17) para reportes al nivel que
// cierra E0-T5: aislamiento por esquema y privilegio mínimo por tabla. Aplica
// las migraciones REALES (0001 estructura + 0002 grants, en orden de nombre)
// sobre una base efímera y comprueba, con escrituras reales ejecutadas como
// svc_reportes, qué permite y qué rechaza Postgres.
//
// reportes es un consumidor TERMINAL (reportes.md (g)): ingiere el flujo vivo y
// proyecta. El criterio del Lead en E0-T5 es SIN DELETE en ninguna tabla; la
// corrida de reconstrucción (ADR 30) tendrá sus propios permisos cuando exista.
//
// Prueba comportamiento observable: hace la escritura y afirma el resultado que
// vería el caso de uso, con el error literal de Postgres en lo denegado.
//
// Se salta si REPORTES_TEST_DB no está definido (CI sin Postgres).
func TestGrants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbtest.Serializar(t, ctx) // no correr a la vez que las demás pruebas de integración (misma base)

	admin, closeAdmin := dbtest.AdminConn(t, ctx)
	defer closeAdmin()

	// Esquema y rol ajenos, efímeros: representan "otro módulo" sin depender de
	// las migraciones de otro repo. svc_reportes NUNCA debe alcanzarlos (I1).
	suffix := fmt.Sprintf("_e0t5_%d", time.Now().UnixNano())
	foreignSchema := "ajeno" + suffix
	foreignRole := "rol_ajeno" + suffix

	cleanup := func() {
		for _, s := range []string{schema, foreignSchema} {
			dbtest.Exec(t, ctx, admin, "DROP SCHEMA IF EXISTS "+pgx.Identifier{s}.Sanitize()+" CASCADE")
		}
		for _, r := range []string{role, foreignRole} {
			dbtest.Exec(t, ctx, admin, "DROP ROLE IF EXISTS "+pgx.Identifier{r}.Sanitize())
		}
	}
	cleanup()
	defer cleanup()

	applyMigrations(t, ctx, admin)

	// "Otro módulo": un esquema con una tabla, y un rol sin ningún GRANT.
	dbtest.Exec(t, ctx, admin, fmt.Sprintf("CREATE SCHEMA %s", foreignSchema))
	dbtest.Exec(t, ctx, admin, fmt.Sprintf("CREATE TABLE %s.dato (id int)", foreignSchema))
	dbtest.Exec(t, ctx, admin, fmt.Sprintf("CREATE ROLE %s NOLOGIN", foreignRole))
	// Una tabla creada DESPUÉS de 0002 (como haría una 0003) no debe heredar
	// privilegios por ALTER DEFAULT PRIVILEGES.
	dbtest.Exec(t, ctx, admin, "CREATE TABLE reportes.tabla_futura (id int)")

	pool, closePool := dbtest.PoolAs(t, ctx, role, schema)
	defer closePool()

	// --- Flujo vivo del consumidor terminal: debe funcionar. ---
	viajeID := "11111111-1111-1111-1111-111111111111"
	eventoID := "22222222-2222-2222-2222-222222222222"

	// Dedup del evento en el inbox (una vez por evento_id).
	mustExec(t, ctx, pool,
		fmt.Sprintf(`INSERT INTO reportes.inbox_evento (evento_id, tipo, consumidor) VALUES ('%s', 'viaje.creado', 'hecho_viaje')`, eventoID),
		"svc_reportes debe poder registrar un evento en su inbox")

	// Proyección por viaje: se inserta y luego avanza con cada evento (UPDATE).
	mustExec(t, ctx, pool,
		fmt.Sprintf(`INSERT INTO reportes.hecho_viaje (viaje_id, tenant_id, estado_actual) VALUES ('%s', 't1', 'SOLICITADO')`, viajeID),
		"svc_reportes debe poder insertar la proyección del viaje")
	mustExec(t, ctx, pool,
		`UPDATE reportes.hecho_viaje SET estado_actual = 'APROBADO', aplicado_hasta = now() WHERE viaje_id = '`+viajeID+`'`,
		"svc_reportes debe poder avanzar la proyección del viaje (UPDATE)")

	// Grano fino de transición: append-only.
	mustExec(t, ctx, pool,
		fmt.Sprintf(`INSERT INTO reportes.hecho_transicion (viaje_id, tenant_id, a_estado, ocurrido_en, evento_id)
		 VALUES ('%s', 't1', 'APROBADO', now(), gen_random_uuid())`, viajeID),
		"svc_reportes debe poder insertar una transición proyectada")

	// Episodio de incidencia: se abre (INSERT) y se cierra (UPDATE de cerrada_en).
	mustExec(t, ctx, pool,
		fmt.Sprintf(`INSERT INTO reportes.hecho_condicion (viaje_id, tenant_id, abierta_en) VALUES ('%s', 't1', now())`, viajeID),
		"svc_reportes debe poder abrir un episodio de condición")
	mustExec(t, ctx, pool,
		`UPDATE reportes.hecho_condicion SET cerrada_en = now() WHERE viaje_id = '`+viajeID+`'`,
		"svc_reportes debe poder cerrar un episodio de condición (UPDATE)")

	// --- SIN DELETE en ninguna tabla (decisión del Lead E0-T5). ---
	denied := []struct{ sql, wantErr, msg string }{
		{`DELETE FROM reportes.inbox_evento`, "permission denied for table inbox_evento",
			"el inbox no se borra"},
		{`UPDATE reportes.inbox_evento SET tipo = 'x'`, "permission denied for table inbox_evento",
			"el inbox no se reescribe (append-only)"},
		{`DELETE FROM reportes.hecho_viaje`, "permission denied for table hecho_viaje",
			"la proyección del viaje no se borra (derivada y permanente)"},
		{`DELETE FROM reportes.hecho_transicion`, "permission denied for table hecho_transicion",
			"el grano de transición no se borra (append-only)"},
		{`UPDATE reportes.hecho_transicion SET a_estado = 'x'`, "permission denied for table hecho_transicion",
			"el grano de transición no se reescribe (append-only)"},
		{`DELETE FROM reportes.hecho_condicion`, "permission denied for table hecho_condicion",
			"el episodio de condición no se borra"},
		{`SELECT 1 FROM reportes.tabla_futura`, "permission denied for table tabla_futura",
			"una tabla futura no hereda privilegios por defecto"},
		{fmt.Sprintf(`SELECT 1 FROM %s.dato`, foreignSchema), "permission denied for schema " + foreignSchema,
			"svc_reportes no alcanza el esquema de otro módulo (I1)"},
	}
	for _, d := range denied {
		mustDeny(t, ctx, pool, d.sql, d.wantErr, d.msg)
	}

	// --- Ningún otro rol alcanza reportes: no hay GRANT a PUBLIC ni a terceros. ---
	foreignPool, closeForeign := dbtest.PoolAs(t, ctx, foreignRole, "")
	defer closeForeign()
	mustDeny(t, ctx, foreignPool, `SELECT 1 FROM reportes.hecho_viaje`, "permission denied for schema reportes",
		"un rol ajeno no alcanza el esquema reportes (I1)")

	// --- svc_reportes tiene USAGE sobre un solo esquema de módulo: el suyo. ---
	assertUsageOnlyOn(t, ctx, admin, role, schema)
}

// applyMigrations aplica, como admin, todos los .sql del esquema en orden de
// nombre: 0001 (estructura) y luego 0002 (grants).
func applyMigrations(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	files, err := fs.Glob(migrations.FS, schema+"/*.sql")
	if err != nil {
		t.Fatalf("listar migraciones: %v", err)
	}
	sort.Strings(files)
	if len(files) < 2 {
		t.Fatalf("se esperaban al menos 0001 y 0002, hay: %v", files)
	}
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

// assertUsageOnlyOn afirma que el rol tiene USAGE solo sobre wantSchema entre
// los esquemas no de sistema. Se excluye `public`: su USAGE llega por el
// pseudo-rol PUBLIC (default de Postgres), es política de la base y no del
// módulo; su REVOKE es tarea del bootstrap de base (R12), no de este 0002.
func assertUsageOnlyOn(t *testing.T, ctx context.Context, admin *pgx.Conn, roleName, wantSchema string) {
	t.Helper()
	rows, err := admin.Query(ctx, `
		SELECT n.nspname FROM pg_namespace n
		 WHERE has_schema_privilege($1, n.oid, 'USAGE')
		   AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
		   AND n.nspname <> 'public'
		 ORDER BY 1`, roleName)
	if err != nil {
		t.Fatalf("consultar USAGE de %s: %v", roleName, err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("leer USAGE de %s: %v", roleName, err)
	}
	if len(got) != 1 || got[0] != wantSchema {
		t.Fatalf("%s debe tener USAGE solo sobre [%s], tiene: %v", roleName, wantSchema, got)
	}
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql, msg string) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func mustDeny(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql, wantErr, msg string) {
	t.Helper()
	_, err := pool.Exec(ctx, sql)
	if err == nil {
		t.Fatalf("%s: la operación tuvo éxito pero debía ser denegada", msg)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("%s: se esperaba un error con %q, se obtuvo: %v", msg, wantErr, err)
	}
}
