// Package migrations expone las migraciones SQL del esquema reportes como un
// embed.FS. La raíz de composición (core) y el arranque standalone (cmd/api)
// las aplican con el rol dueño del esquema (svc_reportes). platform-go no
// administra schemas: cada módulo trae los suyos (00 "Forma de un módulo").
package migrations

import "embed"

// FS contiene los archivos .sql de migración, ordenados por nombre.
//
//go:embed reportes/*.sql
var FS embed.FS
