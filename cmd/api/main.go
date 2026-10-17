// Command api es el arranque standalone de reportes (invariante I5): compila y
// sirve /health y /ready SIN core, cableando el adaptador HTTP hacia catalogos.
// Producción no lo usa para reportes (vive dentro de core); existe para levantar
// y probar el módulo de forma aislada.
//
// Scaffolding: abre el pool de svc_reportes (si hay cadena de conexión),
// construye el módulo con el adaptador HTTP de catalogos y monta las lecturas
// de reportes (E6-T2). Aún no arranca workers (standalone sin bus).
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/AI27-Tech/pf-pkg-platform-go/config"
	"github.com/AI27-Tech/pf-pkg-platform-go/observability"
	"github.com/AI27-Tech/pf-pkg-platform-go/pgxpool"
	"github.com/AI27-Tech/pf-pkg-platform-go/server"
	reportes "github.com/AI27-Tech/pf-svc-reportes"
	catalogoshttp "github.com/AI27-Tech/pf-svc-reportes/adapters/http/catalogos"
)

// Claves de entorno propias de reportes: la cadena de conexión del rol
// svc_reportes y la URL de catalogos. Un esquema, un rol, un pool (ADR 18).
const (
	envDBReportes    = "DB_REPORTES_URL"
	envCatalogosBase = "CATALOGOS_BASE_URL"
	// envTechoFrescura es el umbral de retraso para marcar un reporte stale
	// (reportes.md (i)). Configuración del servicio, no del contrato.
	envTechoFrescura = "REPORTES_TECHO_FRESCURA"
)

func main() {
	if err := run(); err != nil {
		slog.Error("reportes terminó con error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load(envDBReportes, envCatalogosBase)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("arrancando reportes (standalone)", slog.String("service", cfg.ServiceName))

	shutdownOtel, err := observability.Init(ctx, observability.Config{
		ServiceName: cfg.ServiceName,
		ExporterURL: cfg.OtelExporterURL,
	})
	if err != nil {
		logger.Warn("observabilidad no inicializada", slog.Any("error", err))
	} else {
		defer func() { _ = shutdownOtel(context.Background()) }()
	}

	// Un solo pool, su rol (ADR 18 §1): svc_reportes.
	pool, err := pgxpool.New(ctx, "svc_reportes", os.Getenv(envDBReportes))
	if err != nil {
		return err
	}
	defer pool.Close()

	// TODO(reportes): aplicar migrations.FS con el rol svc_reportes antes de
	// servir tráfico. Pendiente del aplicador de migraciones acordado.

	// Adaptador HTTP hacia catalogos (standalone). En core se usa el en proceso.
	cat, err := catalogoshttp.NewCatalogosHTTP(os.Getenv(envCatalogosBase), nil)
	if err != nil {
		return err
	}

	mod, err := reportes.New(reportes.Pools{Reportes: pool}, reportes.Deps{
		Catalogos:     cat,
		TechoFrescura: config.GetEnvDuration(envTechoFrescura, 60*time.Second),
	})
	if err != nil {
		return err
	}

	srv := server.New(cfg.HTTPAddr, server.WithLogger(logger))
	mod.Routes(srv.Mux())
	srv.Registry().RegisterCheck("db_reportes", pool.CheckFunc())

	srv.MarkReady()
	return srv.Start(ctx)
}
