// Package dbtest ofrece utilidades para las pruebas de integración de reportes
// que requieren Postgres. Replica la forma de pf-svc-operacion/internal/dbtest y
// pf-svc-catalogos/internal/dbtest (el dbtest de pf-pkg-platform-go es internal y
// no se puede importar), acotada a lo que reportes necesita: aplicar sus
// migraciones y abrir pools por rol para comprobar los GRANT de E0-T5.
//
// Toda prueba que lo usa se salta (t.Skip) cuando REPORTES_TEST_DB no está
// definido, de modo que `go test ./...` sigue en verde en CI sin Postgres.
//
// REPORTES_TEST_DB debe ser un DSN de una base EFÍMERA con privilegios para
// CREATE ROLE y CREATE SCHEMA (superusuario o equivalente): las pruebas crean y
// eliminan el esquema reportes y el rol svc_reportes.
//
// Ejemplo:
//
//	REPORTES_TEST_DB="postgres://postgres@localhost:5432/postgres" go test ./...
package dbtest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvDSN es la variable de entorno que activa las pruebas de integración de BD.
const EnvDSN = "REPORTES_TEST_DB"

// Serializar toma un lock de sesión de Postgres (pg_advisory_lock) para que las
// pruebas de integración de DISTINTOS paquetes, que comparten una sola base, no
// corran a la vez y se pisen el esquema o el rol (go test corre los paquetes en
// paralelo). El lock se libera al cerrar la conexión dedicada, en el Cleanup de
// la prueba. Toda prueba de integración de reportes debe tomar este mismo lock.
func Serializar(t *testing.T, ctx context.Context) {
	t.Helper()
	conn, err := pgx.Connect(ctx, DSN(t))
	if err != nil {
		t.Fatalf("dbtest: conectar para serializar: %v", err)
	}
	// Clave arbitraria pero COMPARTIDA por todas las pruebas de integración de
	// reportes; cualquier otra que toque la misma base debe tomar esta misma.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(727225)"); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("dbtest: tomar lock de serialización: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) }) // cerrar la conexión libera el lock
}

// DSN devuelve el DSN de administración, o salta la prueba si no está definido.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(EnvDSN)
	if dsn == "" {
		t.Skipf("se salta la prueba de integración: %s no está definido", EnvDSN)
	}
	return dsn
}

// AdminConn abre una conexión de administración para crear o eliminar roles y
// esquemas. El llamador la cierra con la función devuelta.
func AdminConn(t *testing.T, ctx context.Context) (*pgx.Conn, func()) {
	t.Helper()
	conn, err := pgx.Connect(ctx, DSN(t))
	if err != nil {
		t.Fatalf("dbtest: conectar como admin: %v", err)
	}
	return conn, func() { _ = conn.Close(ctx) }
}

// Exec ejecuta una sentencia con la conexión de administración y falla la prueba
// si hay error. Útil para setup y teardown.
func Exec(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("dbtest: exec %q: %v", sql, err)
	}
}

// PoolAs abre un pool que asume el rol dado en cada conexión (SET ROLE), de modo
// que los privilegios efectivos sean los del rol y no los del admin que lo abrió.
// searchPath, si no está vacío, se fija como en producción: el inbox de
// platform-go usa nombres de tabla sin esquema.
func PoolAs(t *testing.T, ctx context.Context, role, searchPath string) (*pgxpool.Pool, func()) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(DSN(t))
	if err != nil {
		t.Fatalf("dbtest: parsear DSN: %v", err)
	}
	if searchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("dbtest: crear pool como %s: %v", role, err)
	}
	return pool, pool.Close
}
