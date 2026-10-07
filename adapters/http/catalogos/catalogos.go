// Package catalogos contiene el adaptador HTTP del puerto CatalogosPort de
// reportes (cliente generado de pf-pkg-contracts). El adaptador EN PROCESO del
// mismo puerto NO vive aquí: vive en pf-svc-core, el único repo que ve a
// reportes y a catalogos a la vez (regla 2 de 00, DEC-050 §2). Este adaptador lo
// usa cmd/api (standalone, I5) y cualquier raíz que no co-ubique catalogos.
package catalogos

import (
	"context"
	"fmt"
	"net/http"

	catclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/catalogos"
	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
	reportes "github.com/AI27-Tech/pf-svc-reportes"
)

// CatalogosHTTP implementa reportes.CatalogosPort con el cliente generado de
// catalogos. Respeta el presupuesto de tiempo (reportes.PresupuestoCatalogos, el
// mismo valor que debe usar el adaptador en proceso de core, I3) y traduce los
// errores a la taxonomía de la plataforma, igual que debe hacer el adaptador en
// proceso.
type CatalogosHTTP struct {
	cli *catclient.ClientWithResponses
}

var _ reportes.CatalogosPort = (*CatalogosHTTP)(nil)

// NewCatalogosHTTP crea el adaptador apuntando a la URL base de catalogos.
func NewCatalogosHTTP(baseURL string, doer catclient.HttpRequestDoer) (*CatalogosHTTP, error) {
	opts := []catclient.ClientOption{}
	if doer != nil {
		opts = append(opts, catclient.WithHTTPClient(doer))
	}
	cli, err := catclient.NewClientWithResponses(baseURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("reportes: crear cliente de catalogos: %w", err)
	}
	return &CatalogosHTTP{cli: cli}, nil
}

// ObtenerContrato lee el contrato vigente a una fecha dada.
func (a *CatalogosHTTP) ObtenerContrato(ctx context.Context, id catclient.Id, aFecha *catclient.MarcaTiempo) (*catclient.Contrato, error) {
	ctx, cancel := context.WithTimeout(ctx, reportes.PresupuestoCatalogos)
	defer cancel()

	resp, err := a.cli.ObtenerContratoWithResponse(ctx, id, &catclient.ObtenerContratoParams{AFecha: aFecha})
	if err != nil {
		return nil, traducirError("ObtenerContrato", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, traducirStatus("ObtenerContrato", resp.StatusCode())
	}
	return resp.JSON200, nil
}

// ObtenerSla lee el SLA vigente a una fecha dada.
func (a *CatalogosHTTP) ObtenerSla(ctx context.Context, id catclient.Id, aFecha *catclient.MarcaTiempo) (*catclient.Sla, error) {
	ctx, cancel := context.WithTimeout(ctx, reportes.PresupuestoCatalogos)
	defer cancel()

	resp, err := a.cli.ObtenerSlaWithResponse(ctx, id, &catclient.ObtenerSlaParams{AFecha: aFecha})
	if err != nil {
		return nil, traducirError("ObtenerSla", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, traducirStatus("ObtenerSla", resp.StatusCode())
	}
	return resp.JSON200, nil
}

// traducirStatus mapea el código HTTP a la taxonomía de la plataforma. Una
// dimensión ausente es no_encontrado; la indisponibilidad de catalogos es
// degradado (reportes marca la frescura y sigue, no se rompe).
func traducirStatus(op string, status int) error {
	switch status {
	case http.StatusNotFound:
		return platformerrors.NewNoEncontrado("catalogos: %s no encontrado", op)
	case http.StatusUnauthorized, http.StatusForbidden:
		return platformerrors.NewNoAutorizado("catalogos: %s no autorizado", op)
	default:
		return platformerrors.NewDegradado("catalogos: %s no disponible (status %d)", op, status)
	}
}

func traducirError(op string, err error) error {
	// Timeout/cancelación o fallo de red: degradado.
	return platformerrors.NewDegradadoErr(err, "reportes: %s contra catalogos falló", op)
}
