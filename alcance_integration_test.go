package reportes

import (
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"
	"time"

	idclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/identidad"
	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/auth"
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

// TestAlcanceFiltraDashboard: el dashboard acota en la consulta (DEC-149,
// reportes.yaml de tracer.42). monitorista y supervisor ven solo los viajes de
// sus clientes efectivos que pasaron por APROBADO; filas, total, conteos por
// estado y frescura salen acotados. Sin alcance proyectado no ven nada. El
// administrador ve todo; jefe_operativo, ejecutivo, reaccion y el cliente
// reciben 403. Mismo resultado por HTTP y por la fachada en proceso.
func TestAlcanceFiltraDashboard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, pool := prepararBD(t, ctx)
	const (
		A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		B = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		// Viajes de A.
		a1 = "a1a1a1a1-0000-0000-0000-000000000001" // nace APROBADO (Operación), sin transiciones
		a3 = "a3a3a3a3-0000-0000-0000-000000000003" // nace APROBADO → ASIGNADO → EN_TRANSITO
		a6 = "a6a6a6a6-0000-0000-0000-000000000006" // nace APROBADO → CANCELADO_POR_CLIENTE
		// Viaje de B, el más reciente (su aplicado_hasta no debe filtrarse a mon-a).
		b1 = "b1b1b1b1-0000-0000-0000-000000000001" // nace APROBADO → ASIGNADO → EN_TRANSITO
	)
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_viaje
		(viaje_id, tenant_id, cliente_id, estado_actual, creado_en, aplicado_hasta) VALUES
		('`+a1+`','`+A+`','`+A+`','APROBADO','2026-10-01T08:00:00Z','2026-10-01T08:00:00Z'),
		('`+a3+`','`+A+`','`+A+`','EN_TRANSITO','2026-10-01T08:20:00Z','2026-10-01T09:20:00Z'),
		('`+a6+`','`+A+`','`+A+`','CANCELADO_POR_CLIENTE','2026-10-01T08:50:00Z','2026-10-01T09:10:00Z'),
		('`+b1+`','`+B+`','`+B+`','EN_TRANSITO','2026-10-01T09:00:00Z','2026-10-01T11:00:00Z')`)
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.hecho_transicion
		(viaje_id, tenant_id, de_estado, a_estado, ocurrido_en, evento_id) VALUES
		('`+a3+`','`+A+`','APROBADO','ASIGNADO','2026-10-01T09:10:00Z',gen_random_uuid()),
		('`+a3+`','`+A+`','ASIGNADO','EN_TRANSITO','2026-10-01T09:20:00Z',gen_random_uuid()),
		('`+a6+`','`+A+`','APROBADO','CANCELADO_POR_CLIENTE','2026-10-01T09:10:00Z',gen_random_uuid()),
		('`+b1+`','`+B+`','APROBADO','ASIGNADO','2026-10-01T10:00:00Z',gen_random_uuid()),
		('`+b1+`','`+B+`','ASIGNADO','EN_TRANSITO','2026-10-01T11:00:00Z',gen_random_uuid())`)
	dbtest.Exec(t, ctx, admin, `INSERT INTO reportes.alcance_sujeto (idp_subject, clientes_ids, cambiado_en) VALUES
		('mon-a', '{`+A+`}', now()), ('sup-ab', '{`+A+`,`+B+`}', now()), ('mon-vacio', '{}', now())`)

	m, err := New(Pools{Reportes: pool}, Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	m.Routes(mux)
	como := func(sub, rol string) context.Context {
		return auth.ContextWithClaims(ctx, &auth.Claims{Subject: sub, Rol: rol, Audiencia: auth.AudienciaInterno})
	}
	hora := func(h, min int) *time.Time { v := time.Date(2026, 10, 1, h, min, 0, 0, time.UTC); return &v }
	desde := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hasta := desde.Add(24 * time.Hour)

	type esperado struct {
		viajes, enCurso []string
		estados         map[string]int
		al              *time.Time // proyeccion_al; nil = null
	}
	todos := esperado{
		viajes:  []string{a1, a3, a6, b1},
		enCurso: []string{a1, a3, b1},
		estados: map[string]int{"ASIGNADO": 2, "EN_TRANSITO": 2, "CANCELADO_POR_CLIENTE": 1},
		al:      hora(11, 0),
	}
	soloA := esperado{
		viajes:  []string{a1, a3, a6},
		enCurso: []string{a1, a3},
		estados: map[string]int{"ASIGNADO": 1, "EN_TRANSITO": 1, "CANCELADO_POR_CLIENTE": 1},
		al:      hora(9, 20), // MAX de a1, a3, a6; no el de b1
	}
	nada := esperado{estados: map[string]int{}}
	casos := []struct {
		nombre string
		ctx    context.Context
		quiere esperado
	}{
		{"administrador: todo", como("adm-1", "administrador"), todos},
		{"monitorista de A: su cliente", como("mon-a", "monitorista"), soloA},
		{"supervisor de A y B: la unión", como("sup-ab", "supervisor"), esperado{
			viajes:  []string{a1, a3, a6, b1},
			enCurso: []string{a1, a3, b1},
			estados: map[string]int{"ASIGNADO": 2, "EN_TRANSITO": 2, "CANCELADO_POR_CLIENTE": 1},
			al:      hora(11, 0),
		}},
		{"coordinador con el alcance de A", como("mon-a", "coordinador"), soloA},
		{"monitorista con alcance vacío: nada", como("mon-vacio", "monitorista"), nada},
		{"monitorista sin alcance proyectado: nada (falla cerrado)", como("mon-sin", "monitorista"), nada},
	}

	getJSON := func(t *testing.T, c context.Context, ruta string, v any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ruta, nil).WithContext(c))
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decodificar %s: %v (%s)", ruta, err, rec.Body.String())
		}
		return rec.Code
	}
	idsViajes := func(t *testing.T, rep *repclient.ReporteViajes) []string {
		t.Helper()
		got := []string{}
		for _, f := range rep.Filas {
			got = append(got, f.ViajeId.String())
		}
		sort.Strings(got)
		if rep.Total != len(got) {
			t.Fatalf("total = %d, filas = %d", rep.Total, len(got))
		}
		return got
	}
	mapaEstados := func(rep *repclient.ReporteEstados) map[string]int {
		got := map[string]int{}
		for _, c := range rep.Conteos {
			got[c.Estado] = c.Conteo
		}
		return got
	}
	mismaAl := func(got *time.Time, quiere *time.Time) bool {
		return (got == nil && quiere == nil) || (got != nil && quiere != nil && got.Equal(*quiere))
	}
	ordenar := func(s []string) []string { s = slices.Clone(s); sort.Strings(s); return s }

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			quiereViajes, quiereEnCurso := ordenar(c.quiere.viajes), ordenar(c.quiere.enCurso)
			if quiereViajes == nil {
				quiereViajes, quiereEnCurso = []string{}, []string{}
			}
			var rv repclient.ReporteViajes
			if code := getJSON(t, c.ctx, "/api/v1/reportes/viajes"+periodoQuery, &rv); code != 200 {
				t.Fatalf("viajes: status %d", code)
			}
			if got := idsViajes(t, &rv); !slices.Equal(got, quiereViajes) {
				t.Errorf("viajes = %v; se esperaba %v", got, quiereViajes)
			}
			if !mismaAl(rv.ProyeccionAl, c.quiere.al) {
				t.Errorf("proyeccion_al = %v; se esperaba %v", rv.ProyeccionAl, c.quiere.al)
			}
			var rc repclient.ReporteViajes
			if code := getJSON(t, c.ctx, "/api/v1/reportes/viajes?en_curso=true", &rc); code != 200 {
				t.Fatalf("en_curso: status %d", code)
			}
			if got := idsViajes(t, &rc); !slices.Equal(got, quiereEnCurso) {
				t.Errorf("en_curso = %v; se esperaba %v", got, quiereEnCurso)
			}
			var re repclient.ReporteEstados
			if code := getJSON(t, c.ctx, "/api/v1/reportes/estados"+periodoQuery, &re); code != 200 {
				t.Fatalf("estados: status %d", code)
			}
			if got := mapaEstados(&re); !maps.Equal(got, c.quiere.estados) {
				t.Errorf("estados = %v; se esperaba %v", got, c.quiere.estados)
			}

			// La fachada en proceso (core) da lo mismo (I3).
			fv, err := m.Consultas().ReporteViajes(c.ctx, repclient.ReporteViajesParams{Desde: &desde, Hasta: &hasta})
			if err != nil {
				t.Fatalf("fachada ReporteViajes: %v", err)
			}
			if got := idsViajes(t, fv); !slices.Equal(got, quiereViajes) {
				t.Errorf("fachada viajes = %v; se esperaba %v", got, quiereViajes)
			}
			fe, err := m.Consultas().ReporteEstados(c.ctx, repclient.ReporteEstadosParams{Desde: desde, Hasta: hasta})
			if err != nil {
				t.Fatalf("fachada ReporteEstados: %v", err)
			}
			if got := mapaEstados(fe); !maps.Equal(got, c.quiere.estados) {
				t.Errorf("fachada estados = %v; se esperaba %v", got, c.quiere.estados)
			}
		})
	}

	// Los roles sin el dashboard reciben 403 aunque tengan alcance proyectado.
	for _, rol := range []string{"jefe_operativo", "ejecutivo", "reaccion"} {
		t.Run(rol+": 403", func(t *testing.T) {
			c := como("mon-a", rol)
			for _, ruta := range []string{"/api/v1/reportes/viajes" + periodoQuery, "/api/v1/reportes/estados" + periodoQuery} {
				var body repclient.Error
				if code := getJSON(t, c, ruta, &body); code != 403 || body.Codigo != "prohibido" {
					t.Errorf("%s = (%d, %q); se esperaba (403, prohibido)", ruta, code, body.Codigo)
				}
			}
		})
	}
}
