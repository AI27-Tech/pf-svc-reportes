-- 0004_alcance_sujeto.sql — proyección local de `alcance.cambiado` (DEC-149
-- #20, #22; tracer.42): los clientes efectivos de cada sujeto según identidad
-- ("mis clientes" más los de sus subordinados). Con ella reportes acota el
-- dashboard de monitorista, supervisor y coordinador por hecho_viaje.cliente_id
-- (DEC-053 #3).
--
-- Vive en Postgres y no en memoria porque el consumidor es durable y comparte
-- grupo entre réplicas: cada evento llega a UNA réplica. En el esquema reportes
-- para que svc_reportes la lea en la misma conexión que las proyecciones (I1,
-- sin pool ni GRANT cruzado nuevos).
--
-- Cada fila es el conjunto COMPLETO (snapshot, no delta); vacío = el sujeto no
-- ve viajes. cambiado_en ordena: un alcance más viejo que el guardado no gana.
-- Sin DELETE (0002): quitar a un sujeto es dejar su conjunto vacío, conservando
-- cambiado_en para que un evento atrasado no lo reviva.
--
-- La llave es idp_subject, el `sub` del token del sujeto: el `sub` es opaco y
-- distinto del sujeto_id (DEC-072). TEXT porque el subject del IdP no es UUID.

CREATE TABLE IF NOT EXISTS reportes.alcance_sujeto (
  idp_subject  TEXT        PRIMARY KEY,
  clientes_ids UUID[]      NOT NULL DEFAULT '{}',
  cambiado_en  TIMESTAMPTZ NOT NULL
);

-- Grants: 0002 retiró los privilegios por defecto, así que la tabla declara los
-- suyos. SELECT para leer el alcance; INSERT y UPDATE para proyectar. Sin DELETE.
GRANT SELECT, INSERT, UPDATE ON reportes.alcance_sujeto TO svc_reportes;
