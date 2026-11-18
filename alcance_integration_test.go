package reportes

import (
	"context"
	"io/fs"
	"slices"
	"sort"
	"testing"
	"time"

	idclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/identidad"
	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
	"github.com/AI27-Tech/pf-svc-reportes/internal/dbtest"
	"github.com/AI27-Tech/pf-svc-reportes/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// prepararBD aplica las migraciones reales sobre un esquema limpio y devuelve la
// conexión de admin (para sembrar) y un pool que corre como svc_reportes. Se
// salta si REPORTES_TEST_DB no está definido.
func prepararBD(t *testing.T, ctx context.Context) (*pgx.Conn, *pgxpool.Pool) {
	t.Helper()
	dbtest.Serializar(t, context.Background())
	admin, closeAdmin := dbtest.AdminConn(t, context.Background())
	t.Cleanup(closeAdmin)
	// El cleanup corre después de que la prueba cancela su ctx: usa uno propio.
	cleanup := func() {
		bg := context.Background()
		dbtest.Exec(t, bg, admin, "DROP SCHEMA IF EXISTS reportes CASCADE")
		dbtest.Exec(t, bg, admin, "DROP ROLE IF EXISTS svc_reportes")
	}
	cleanup()
	t.Cleanup(cleanup)
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
	raw, closeRaw := dbtest.PoolAs(t, context.Background(), "svc_reportes", "reportes")
	t.Cleanup(closeRaw) // registrado después de cleanup: corre antes (LIFO)
	return admin, &pgxpool.Pool{Pool: raw, Role: "svc_reportes"}
}

func sobreAlcance(sub string, en time.Time, clientes ...uuid.UUID) bus.Sobre {
	ids := `[`
	for i, c := range clientes {
		if i > 0 {
			ids += `,`
		}
		ids += `"` + c.String() + `"`
	}
	ids += `]`
	return bus.Sobre{
		EventoID: uuid.NewString(), Tipo: "alcance.cambiado", Version: 1, AgregadoID: uuid.NewString(),
		Payload: []byte(`{"sujeto_id":"` + uuid.NewString() + `","idp_subject":"` + sub + `","clientes_efectivos_ids":` + ids + `,"cambiado_en":"` + en.Format(time.RFC3339Nano) + `"}`),
	}
}

func clientesDe(t *testing.T, ctx context.Context, m *Module, sub string) []uuid.UUID {
	t.Helper()
	got, err := m.alcance.Clientes(ctx, sub)
	if err != nil {
		t.Fatalf("Clientes(%s): %v", sub, err)
	}
	slices.SortFunc(got, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	return got
}

func ordenados(ids ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if out == nil {
		out = []uuid.UUID{}
	}
	return out
}

// TestAlcanceProyeccionDesdeEvento: el handler que New suscribe a
// alcance.cambiado proyecta el conjunto completo por idp_subject. Un evento más
// viejo no gana, la reentrega no cambia nada, el conjunto vacío quita al sujeto,
// y un payload sin idp_subject (tracer.41) se descarta sin error.
func TestAlcanceProyeccionDesdeEvento(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, pool := prepararBD(t, ctx)
	sub := &subscriberFake{}
	m, err := New(Pools{Reportes: pool}, Deps{Subscriber: sub})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := sub.subs["reportes-alcance"].h
	A, B := uuid.New(), uuid.New()
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	procesar := func(s bus.Sobre) {
		t.Helper()
		if err := h(ctx, s); err != nil {
			t.Fatalf("handler: %v", err)
		}
	}

	if got := clientesDe(t, ctx, m, "mon-a"); len(got) != 0 {
		t.Fatalf("sin evento: %v; se esperaba vacío (falla cerrado)", got)
	}
	procesar(sobreAlcance("mon-a", t0, A, B))
	if got := clientesDe(t, ctx, m, "mon-a"); !slices.Equal(got, ordenados(A, B)) {
		t.Fatalf("tras el evento: %v; se esperaba [A B]", got)
	}
	reentrega := sobreAlcance("mon-a", t0.Add(time.Minute), A)
	procesar(reentrega)
	procesar(reentrega)
	if got := clientesDe(t, ctx, m, "mon-a"); !slices.Equal(got, []uuid.UUID{A}) {
		t.Fatalf("snapshot más nuevo (y reentregado): %v; se esperaba [A]", got)
	}
	procesar(sobreAlcance("mon-a", t0, A, B)) // atrasado
	if got := clientesDe(t, ctx, m, "mon-a"); !slices.Equal(got, []uuid.UUID{A}) {
		t.Fatalf("evento atrasado ganó: %v; se esperaba [A]", got)
	}
	procesar(sobreAlcance("mon-a", t0.Add(2*time.Minute)))
	if got := clientesDe(t, ctx, m, "mon-a"); len(got) != 0 {
		t.Fatalf("conjunto vacío: %v; se esperaba quitar al sujeto", got)
	}

	sinSub := bus.Sobre{EventoID: uuid.NewString(), Tipo: "alcance.cambiado",
		Payload: []byte(`{"sujeto_id":"` + uuid.NewString() + `","clientes_efectivos_ids":["` + A.String() + `"],"cambiado_en":"2026-11-17T11:00:00Z"}`)}
	procesar(sinSub)
	var filas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reportes.alcance_sujeto`).Scan(&filas); err != nil || filas != 1 {
		t.Fatalf("payload sin idp_subject: %d filas (err %v); se esperaba solo la de mon-a", filas, err)
	}
}

// workerHastaCancelar es la suscripción desnuda: Run bloquea hasta cancelar.
type workerHastaCancelar struct{}

func (workerHastaCancelar) Name() string                  { return "bus-durable:reportes-alcance" }
func (workerHastaCancelar) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (workerHastaCancelar) Ready(context.Context) error   { return nil }

// subscriberBloqueante devuelve workers que bloquean hasta cancelar, para correr
// la reconstrucción como lo haría core.
type subscriberBloqueante struct{}

func (subscriberBloqueante) Subscribe(string, bus.Sensibilidad, []string, bus.Handler) (worker.Worker, error) {
	return workerHastaCancelar{}, nil
}

// TestAlcanceReconstruccionSnapshotYCursor: al arrancar, el snapshot reemplaza
// la proyección (vacía a los ausentes) y la continuación con el cursor aplica lo
// cambiado después, donde un item vacío quita al sujeto. Ready es not-ready
// hasta converger.
func TestAlcanceReconstruccionSnapshotYCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, pool := prepararBD(t, ctx)
	A, B, C := uuid.New(), uuid.New(), uuid.New()
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// Estado previo (proyección rancia de una corrida anterior).
	if _, err := admin.Exec(ctx, `INSERT INTO reportes.alcance_sujeto (idp_subject, clientes_ids, cambiado_en) VALUES
		('mon-viejo', $1, $3), ('mon-b', $2, $3)`, []uuid.UUID{A}, []uuid.UUID{B}, t0.Add(-time.Hour)); err != nil {
		t.Fatalf("sembrar alcance previo: %v", err)
	}
	cursor := "c1"
	snap := &alcancesFake{
		snapshot: &idclient.PaginaAlcances{CursorSiguiente: &cursor, Items: []idclient.AlcanceEfectivo{
			{IdpSubject: "mon-a", ClientesEfectivosIds: []uuid.UUID{A}, CambiadoEn: t0},
			{IdpSubject: "mon-b", ClientesEfectivosIds: []uuid.UUID{B}, CambiadoEn: t0},
		}},
		continuacion: &idclient.PaginaAlcances{Items: []idclient.AlcanceEfectivo{
			{IdpSubject: "mon-b", ClientesEfectivosIds: []uuid.UUID{}, CambiadoEn: t0.Add(time.Minute)}, // quitar
			{IdpSubject: "sup-ac", ClientesEfectivosIds: []uuid.UUID{A, C}, CambiadoEn: t0.Add(time.Minute)},
		}},
	}
	m, err := New(Pools{Reportes: pool}, Deps{Subscriber: subscriberBloqueante{}, Alcances: snap})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var rec worker.Worker
	for _, w := range m.Workers() {
		if w.Name() == "reportes-alcance" {
			rec = w
		}
	}
	if rec == nil {
		t.Fatal("falta el worker reportes-alcance")
	}
	if rec.Ready(ctx) == nil {
		t.Fatal("antes de correr: Ready nil; se esperaba not-ready")
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- rec.Run(runCtx) }()
	for rec.Ready(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatalf("no convergió: %v", rec.Ready(context.Background()))
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	for sub, quiere := range map[string][]uuid.UUID{
		"mon-a":     ordenados(A),
		"mon-b":     ordenados(), // en el snapshot, quitado por la continuación
		"sup-ac":    ordenados(A, C),
		"mon-viejo": ordenados(), // ausente del snapshot: vaciado
	} {
		if got := clientesDe(t, ctx, m, sub); !slices.Equal(got, quiere) {
			t.Errorf("%s: %v; se esperaba %v", sub, got, quiere)
		}
	}
}
