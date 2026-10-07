// Package dimcatalogo materializaría la dimensión dim_catalogo a partir de
// catalogo.actualizado más la relectura a una fecha de catalogos (reportes.md
// (e), ADR 30 §1).
//
// BLOQUEADO POR R8 Y R11:
//   - R8: catalogo.actualizado no lista a reportes como consumidor durable en el
//     inventario y el mensaje no lleva versión (misma brecha que C10 de 02).
//   - R11: falta la lectura POR LOTE de catalogos; solo existe a una fecha para
//     contrato y SLA (brechas C4/C5/C9 de 02).
//
// Contrato primero (00 regla 6): no se crea tabla, consumidor ni tipo. Cuando R8
// y R11 existan, aquí vive el versionado de dim_catalogo y la invalidación de
// caché por catalogo.actualizado.
//
// TODO(reportes): implementar al cerrarse R8 y R11.
package dimcatalogo
