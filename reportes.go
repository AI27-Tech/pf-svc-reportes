// Package reportes responde preguntas analíticas y agregadas sobre la operación
// y NO es dueño de ningún hecho (reportes.md (a)). Mantiene un modelo de lectura
// derivado —proyecciones propias alimentadas por los eventos versionados de sus
// productores (ADR 18, cerrado)— bajo su propio esquema, rol y compuerta de
// migración. Si una proyección discrepa de su origen, gana el origen
// (reportes.md (e), (h) R5).
//
// Capa de soporte. Vive en el deployable `core` (00, tabla de topología). Su eje
// de escalamiento es complejidad de consulta y memoria; la forma de instancia
// con más memoria no se puede satisfacer en V1 mientras viva en `core`
// (reportes.md (k), ADR 17), y se atiende el día de la extracción.
//
// Aristas síncronas salientes: hacia `catalogos` (lectura por lote y a una
// fecha, reportes.md (f)) y la restringida a `identidad` para revocación
// (listarRevocadas + obtenerSesion, DEC-051). NO llama a `operacion` ni a
// `procesamiento`: esos hechos llegan por evento. Arista entrante: solo desde
// `bff` (P16).
//
// Consumidor terminal: no publica eventos, no tiene `outbox` ni relay
// (reportes.md (g)).
//
// Scaffolding inicial: el cableado y la estructura existen; la lógica de negocio
// real todavía no. Varias superficies están bloqueadas por brechas de contrato
// (ver README, R3–R12): sus rutas, tablas y métodos no se montan hasta que el
// contrato las exponga.
package reportes

import (
	"context"
	"net/http"

	repclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/reportes"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-pkg-platform-go/worker"
)

// Pools agrupa los pools por rol que el módulo necesita. reportes usa un solo
// esquema y un solo rol (`svc_reportes`), así que recibe un único pool (00 regla
// 4, ADR 18 §1). Nunca comparte pool entre roles.
type Pools struct {
	Reportes *pgxpool.Pool
}

// Deps agrupa los puertos de salida que la raíz resuelve por el consumidor.
type Deps struct {
	// Catalogos es la arista a catalogos: lectura por lote y a una fecha de las
	// dimensiones (reportes.md (f)). Puede ser nil en el esqueleto; sin ella las
	// proyecciones que necesiten dimensión quedan sin resolver.
	Catalogos CatalogosPort
	// Revocacion es la arista restringida a identidad (DEC-051). Opcional en el
	// esqueleto; sin ella la autorización corre sin proyección de tombstones.
	Revocacion RevocacionPort
}

// Module es la instancia construida del módulo reportes.
type Module struct {
	pools Pools
	deps  Deps
	// Aquí vivirán la puerta de presupuesto, las proyecciones y los consumidores
	// cuando se implementen.
}

// New recibe el pool de svc_reportes y los puertos ya resueltos. No lee entorno
// (00 regla 4): la raíz cablea todo.
func New(p Pools, d Deps) (*Module, error) {
	return &Module{pools: p, deps: d}, nil
}

// Routes monta las rutas HTTP del módulo bajo /api/v1/reportes/... en el mux
// dado. Scaffolding: hoy NO monta ninguna ruta de reporte. Las operaciones
// reporteViajes y reporteEstados existen en el contrato (R2), pero la MARCA DE
// FRESCURA no está en sus schemas (R3) y la frescura no es opcional
// (reportes.md (i)): sin ella no se monta la ruta. Las demás rutas
// (/cumplimiento, /flota, /exportaciones) no existen en el contrato (R4–R6).
func (m *Module) Routes(mux *http.ServeMux) {
	// TODO(reportes): montar reporteViajes y reporteEstados cuando R3 (frescura
	// contractual) exista, delegando en los mismos casos de uso que Consultas()
	// (I3). No montar /cumplimiento, /flota ni /exportaciones hasta R4–R6.
}

// Workers devuelve los workers embebidos del módulo (ADR 29 §6), cada uno con el
// rol y pool de svc_reportes: (1) el consumidor de proyecciones, (2) el
// consumidor de sesion.revocada (suscripción durable propia, DEC-057), y (3) el
// reconciliador (comparación periódica proyección contra origen) como esqueleto,
// porque su fuente de comparación depende de la reconstrucción (R12). Ningún
// worker transversal ni acceso a esquemas ajenos.
//
// Scaffolding: hoy devuelve una lista vacía. Los consumidores se cablean cuando
// la raíz inyecte el adaptador de bus (bus/memory en desarrollo; broker real en
// espera de ADR 32) y cada suscripción durable propia exista.
func (m *Module) Workers() []worker.Worker {
	return nil
}

// Consultas expone los casos de uso que bff consume en proceso (arista síncrona
// entrante). Tipada SOLO con tipos de pf-pkg-contracts, nunca con tipos de
// internal/.
func (m *Module) Consultas() ConsultasAPI {
	return &consultas{m: m}
}

// ConsultasAPI es la superficie síncrona de reportes. Cada método corresponde
// 1:1 a un operationId presente en reportes.yaml de la versión fijada (00 regla
// 6): reporteViajes y reporteEstados. Tipada con los tipos generados del
// paquete reportes de pf-pkg-contracts.
//
// IMPORTANTE: aunque las dos operaciones existen en el contrato (R2), sus
// schemas de respuesta (ReporteViajes, ReporteEstados) NO incluyen la marca de
// frescura (reportes.md (i)), que es obligatoria y el contrato todavía no
// declara (R3). Por eso los métodos se exponen pero devuelven "no implementado"
// hasta R3; el caso de uso interno ya se estructura para producir frescura en su
// resultado, y la ruta no se monta (ver Routes).
type ConsultasAPI interface {
	ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error)
	ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error)
}

type consultas struct {
	m *Module
}

func (c *consultas) ReporteViajes(ctx context.Context, params repclient.ReporteViajesParams) (*repclient.ReporteViajes, error) {
	// Bloqueado por R3: la marca de frescura no está en el schema de respuesta.
	return nil, errNoImplementado("ReporteViajes")
}

func (c *consultas) ReporteEstados(ctx context.Context, params repclient.ReporteEstadosParams) (*repclient.ReporteEstados, error) {
	// Bloqueado por R3: la marca de frescura no está en el schema de respuesta.
	return nil, errNoImplementado("ReporteEstados")
}
