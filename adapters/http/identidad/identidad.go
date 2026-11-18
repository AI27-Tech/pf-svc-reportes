// Package identidad contiene el adaptador HTTP del puerto AlcancesPort de
// reportes (identidad.listarAlcances, DEC-149 #20), con el cliente generado de
// pf-pkg-contracts. El adaptador EN PROCESO del mismo puerto vive en
// pf-svc-core (la sub-fachada Alcances() de identidad), igual que en catalogos.
package identidad

import (
	"context"
	"fmt"
	"net/http"

	idclient "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/identidad"
	platformerrors "github.com/AI27-Tech/pf-pkg-platform-go/errors"
	reportes "github.com/AI27-Tech/pf-svc-reportes"
)

// AlcancesHTTP implementa reportes.AlcancesPort. listarAlcances exige un token
// de servicio válido: el doer que inyecta la raíz lo agrega. El presupuesto de
// tiempo lo pone el llamador (la reconstrucción), igual que en proceso (I3).
type AlcancesHTTP struct {
	cli *idclient.ClientWithResponses
}

var _ reportes.AlcancesPort = (*AlcancesHTTP)(nil)

// NewAlcancesHTTP crea el adaptador apuntando a la URL base de identidad.
func NewAlcancesHTTP(baseURL string, doer idclient.HttpRequestDoer) (*AlcancesHTTP, error) {
	opts := []idclient.ClientOption{}
	if doer != nil {
		opts = append(opts, idclient.WithHTTPClient(doer))
	}
	cli, err := idclient.NewClientWithResponses(baseURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("reportes: crear cliente de identidad: %w", err)
	}
	return &AlcancesHTTP{cli: cli}, nil
}

// ListarAlcances pide el snapshot (sin cursor) o su continuación (con cursor).
func (a *AlcancesHTTP) ListarAlcances(ctx context.Context, params idclient.ListarAlcancesParams) (*idclient.PaginaAlcances, error) {
	resp, err := a.cli.ListarAlcancesWithResponse(ctx, &params)
	if err != nil {
		return nil, platformerrors.NewDegradadoErr(err, "reportes: ListarAlcances contra identidad falló")
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		if resp.JSON200 == nil {
			return nil, platformerrors.NewDegradado("identidad: listarAlcances 200 sin cuerpo")
		}
		return resp.JSON200, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, platformerrors.NewNoAutorizado("identidad: no autorizado")
	default:
		return nil, platformerrors.NewDegradado("identidad: respuesta no disponible (status %d)", resp.StatusCode())
	}
}
