// Package exportacion alojaría la exportación de reportes, que —de existir en
// V1— sería ASÍNCRONA (un recurso consultable) y fallaría si excediera el techo
// de frescura (reportes.md (f), (i)).
//
// BLOQUEADO POR R6: el contrato no tiene POST /api/v1/exportaciones ni
// GET /api/v1/exportaciones/{id}, y declara la exportación masiva FUERA DE V1
// (DEC-023/036). La descarga de UN viaje es E6-T3 y vive en bff + operacion, no
// aquí. Contrato primero (00 regla 6): no se crea ruta ni tabla.
//
// LFPDPPP: la exportación es la superficie por donde saldrían datos personales
// de custodios (reportes.md (l)); su resolución queda pendiente y no se decide
// en el scaffold.
//
// TODO(reportes): implementar solo si R6 se contrata, asíncrona y con techo de
// frescura.
package exportacion
