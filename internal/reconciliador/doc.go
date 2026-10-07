// Package reconciliador compara periódicamente las proyecciones de reportes
// contra su origen por muestreo (reportes.md (g), (j)): la consistencia eventual
// se MIDE y se declara, nunca se oculta. Ante discrepancia gana el origen
// (reportes.md (e), (h) R5); el reconciliador no corrige en silencio, reporta.
//
// Es un worker embebido más (ADR 29 §6), con el pool de svc_reportes.
//
// BLOQUEADO POR R12: su fuente de comparación es la reconstrucción desde los
// productores (ADR 30), cuyos contratos de corrida no existen todavía. Por eso
// el scaffold lo deja como esqueleto: el worker se declara pero no compara nada
// hasta R12.
//
// TODO(reportes): implementar la comparación por muestreo al cerrarse R12,
// reutilizando el punto de extensión de internal/consumidores.
package reconciliador
