-- 0003_hecho_viaje_unidades.sql — alinea hecho_viaje con la escolta por unidades.
--
-- Por qué: viaje.asignado/.reasignado solo llevan `unidades_ids` (lista de
-- paquetes del roster); los campos `unidad_id`/`custodios_ids` quedaron
-- `reserved` en viaje.proto por la ruptura tracer.4 (ADR 31 / DEC-044): la
-- asignación reserva una ESCOLTA de una o más unidades, no "unidad + custodios".
-- El esquema 0001 se escribió antes de esa ruptura, con `unidad_id UUID` (uno
-- solo) + `custodios_ids UUID[]`, que ningún evento contratado puebla.
--
-- Qué hace: agrega `unidades_ids UUID[]` para proyectar la lista tal como llega
-- en el evento. No se otorga grant: una columna hereda los privilegios de su
-- tabla, y hecho_viaje ya tiene INSERT/UPDATE para svc_reportes (0002).
--
-- Deuda nombrada: `unidad_id` y `custodios_ids` quedan como columnas muertas (sin
-- productor ni lector). Su retiro es un refactor de esquema aparte, fuera de este
-- slice de lado escritura (E6-T1).

ALTER TABLE reportes.hecho_viaje
  ADD COLUMN IF NOT EXISTS unidades_ids UUID[] NOT NULL DEFAULT '{}';
