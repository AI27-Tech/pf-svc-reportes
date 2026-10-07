package reportes

import (
	"context"
	"time"

	catclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/catalogos"
)

// Puertos de salida que declara reportes COMO CONSUMIDOR (regla 2 de 00-indice,
// ADR 17). La interfaz la declara el consumidor para que pf-svc-core la
// implemente con el adaptador en proceso y este repo con el adaptador HTTP. Los
// tipos de entrada/salida son los generados de pf-pkg-contracts, nunca escritos
// a mano (regla 6).

// PresupuestoCatalogos es el presupuesto de tiempo de la arista a catalogos. Se
// exporta JUNTO AL PUERTO porque la obligación I3 (ADR 17) exige que el
// adaptador HTTP de este repo y el en proceso de pf-svc-core usen EXACTAMENTE el
// mismo valor; core lo lee de aquí y no duplica el número. Si debe configurarse
// por despliegue, pasa a Deps/config conservando este valor como default común.
const PresupuestoCatalogos = 2 * time.Second

// CatalogosPort es la arista síncrona hacia catalogos para las dimensiones de
// las proyecciones (reportes.md (f)). Debe leer POR LOTE y A UNA FECHA, nunca
// por elemento (evita N+1): un reporte toca muchas dimensiones a la vez.
//
// Solo se declaran operaciones que EXISTEN en catalogos.yaml de la versión
// fijada (regla 6). La lectura a una fecha existe para contrato y SLA; la
// lectura POR LOTE de átomos y de clientes NO está en el contrato (brechas C4,
// C5, C9 de 02 → R11). Por eso este puerto queda hoy con la forma mínima
// verificable y se amplía cuando el contrato exponga el lote.
//
// BRECHA R11 (no se corrige aquí; contrato primero): falta la lectura por lote
// de dimensiones. El puerto no inventa esos métodos; `dim_catalogo` queda
// bloqueada (ver internal/dimcatalogo/doc.go).
type CatalogosPort interface {
	// ObtenerContrato y ObtenerSla leen una dimensión a una fecha dada (vigencia),
	// que es lo que el contrato expone hoy. Son el punto de partida verificable;
	// la lectura por lote se agrega con R11.
	ObtenerContrato(ctx context.Context, id catclient.Id, aFecha *catclient.MarcaTiempo) (*catclient.Contrato, error)
	ObtenerSla(ctx context.Context, id catclient.Id, aFecha *catclient.MarcaTiempo) (*catclient.Sla, error)
}

// RevocacionPort es la arista RESTRINGIDA hacia identidad (DEC-051): solo dos
// operaciones, para el snapshot al arrancar (ADR 26 §4) y el fallback en modo
// degradado (ADR 26 §5). reportes verifica tokens como todo módulo protegido;
// no es una dependencia de dominio sino de infraestructura de autorización. El
// adaptador HTTP vive en adapters/http/; el en proceso, en pf-svc-core.
type RevocacionPort interface {
	// ListarRevocadas devuelve las sesiones revocadas para el snapshot inicial.
	ListarRevocadas(ctx context.Context) ([]string, error)
	// ObtenerSesion consulta una sesión puntual tras 15 s sin frescura.
	ObtenerSesion(ctx context.Context, sesionID string) (revocada bool, err error)
}

// PresupuestoRevocacion es el presupuesto de tiempo de la arista restringida a
// identidad (RevocacionPort). Se exporta JUNTO AL PUERTO por la obligación I3
// (ADR 17): el adaptador HTTP de este repo y el EN PROCESO de pf-svc-core deben
// usar EXACTAMENTE el mismo valor; core lo lee de aquí y no duplica el número.
//
// Es el presupuesto de UNA llamada puntual (snapshot al arrancar o fallback
// ObtenerSesion), no el umbral de frescura del canal: ese umbral (15 s sin
// frescura antes del fallback, 30 s de corte máximo) lo fija ADR 26 y es
// independiente del timeout de una sola llamada HTTP. El valor concreto no está
// en las fuentes; 2 s es el default común, igual que PresupuestoCatalogos, y no
// es un número de negocio aprobado.
const PresupuestoRevocacion = 2 * time.Second
