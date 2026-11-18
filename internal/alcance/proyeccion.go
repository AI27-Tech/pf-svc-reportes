package alcance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DB es lo que la proyección usa del pool de svc_reportes (la cumple
// *pgxpool.Pool de pf-pkg-platform-go).
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Alcance son los clientes efectivos de un sujeto en un instante: el payload de
// alcance.cambiado y el item de identidad.listarAlcances (DEC-149 #20).
type Alcance struct {
	// IdpSubject es el `sub` del token del sujeto (DEC-149 #22): la llave con
	// que la lectura busca el alcance de quien hace la petición.
	IdpSubject string
	Clientes   []uuid.UUID
	CambiadoEn time.Time
}

// Proyeccion es reportes.alcance_sujeto (reportes/0004_alcance_sujeto.sql).
// Cada fila es el conjunto COMPLETO de un sujeto (snapshot, no delta). Aplicar
// el mismo alcance dos veces deja el mismo estado, y uno más viejo que el
// guardado no gana (orden por cambiado_en), así que reentregas y desorden
// convergen.
type Proyeccion struct {
	db DB
}

// NuevaProyeccion crea la proyección sobre el pool de svc_reportes.
func NuevaProyeccion(db DB) *Proyeccion { return &Proyeccion{db: db} }

// Clientes devuelve los clientes efectivos del sujeto. Un sujeto sin fila no ve
// nada (lista vacía, no nil): falla cerrado. Un error de base se propaga; nunca
// se interpreta como "todos".
func (p *Proyeccion) Clientes(ctx context.Context, idpSubject string) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	err := p.db.QueryRow(ctx,
		`SELECT clientes_ids FROM reportes.alcance_sujeto WHERE idp_subject = $1`, idpSubject).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return []uuid.UUID{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reportes: leer el alcance del sujeto: %w", err)
	}
	return out, nil
}

// upsert guarda a si no es más viejo que lo guardado. `>=` y no `>`: el mismo
// instante es el mismo cambio, y así la continuación del snapshot repone lo que
// Reemplazar haya vaciado de más (ver Reemplazar).
const upsert = `INSERT INTO reportes.alcance_sujeto (idp_subject, clientes_ids, cambiado_en)
	VALUES ($1, $2, $3)
	ON CONFLICT (idp_subject) DO UPDATE
	   SET clientes_ids = EXCLUDED.clientes_ids, cambiado_en = EXCLUDED.cambiado_en
	 WHERE reportes.alcance_sujeto.cambiado_en <= EXCLUDED.cambiado_en`

// Aplicar guarda un alcance (evento o continuación del snapshot). Un conjunto
// vacío se guarda como tal: quita al sujeto sin perder su cambiado_en.
func (p *Proyeccion) Aplicar(ctx context.Context, a Alcance) error {
	return p.enTx(ctx, func(tx pgx.Tx) error { return aplicar(ctx, tx, a) })
}

// Reemplazar carga el snapshot inicial: guarda cada item y vacía a todo sujeto
// que no venga en él (identidad solo trae los no vacíos). Vaciar es fallar
// cerrado: si un evento más nuevo ya había llegado para un sujeto ausente, la
// continuación del snapshot lo repone (lleva todo lo cambiado después). Sin
// DELETE: svc_reportes no lo tiene (0002_grants).
func (p *Proyeccion) Reemplazar(ctx context.Context, items []Alcance) error {
	return p.enTx(ctx, func(tx pgx.Tx) error {
		presentes := make([]string, 0, len(items))
		for _, a := range items {
			if err := aplicar(ctx, tx, a); err != nil {
				return err
			}
			presentes = append(presentes, a.IdpSubject)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE reportes.alcance_sujeto SET clientes_ids = '{}'
			  WHERE idp_subject <> ALL($1::text[]) AND cardinality(clientes_ids) > 0`, presentes); err != nil {
			return fmt.Errorf("reportes: vaciar alcances fuera del snapshot: %w", err)
		}
		return nil
	})
}

func (p *Proyeccion) enTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reportes: abrir transacción de alcance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func aplicar(ctx context.Context, tx pgx.Tx, a Alcance) error {
	clientes := a.Clientes
	if clientes == nil {
		clientes = []uuid.UUID{}
	}
	if _, err := tx.Exec(ctx, upsert, a.IdpSubject, clientes, a.CambiadoEn.UTC()); err != nil {
		return fmt.Errorf("reportes: guardar el alcance de %s: %w", a.IdpSubject, err)
	}
	return nil
}
