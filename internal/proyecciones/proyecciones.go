// Package proyecciones aloja los casos de uso de lectura de reportes, uno por
// proyección V1 (hecho_viaje, hecho_transicion, hecho_condicion). Cada caso de
// uso devuelve, junto con sus datos, la MARCA DE FRESCURA de las proyecciones
// que usó —hasta qué evento se aplicó (reportes.md (i))—: la frescura es parte
// del resultado, no un detalle interno. Sin ella, un reporte congelado por caída
// del broker se vería igual que uno de un periodo tranquilo.
//
// Todo caso de uso filtra por tenant ANTES de agregar (P9): un conteo o promedio
// sobre dos tenants filtra sin devolver una sola fila ajena. Ninguna consulta es
// libre: el llamador envía parámetros a reportes definidos, nunca SQL.
//
// Scaffolding: los casos de uso se declaran con su forma (incluida la frescura)
// pero devuelven "no implementado"; la lectura real contra el pool de
// svc_reportes se cablea después. Las RUTAS y los métodos de Consultas() del
// módulo NO se montan hasta que el contrato declare la frescura en la respuesta
// (R3): aquí la frescura ya existe en el resultado interno para que, al cerrarse
// R3, solo falte exponerla.
package proyecciones

import (
	"time"
)

// Frescura declara hasta qué punto está al día una proyección (reportes.md (i)).
// Es parte de todo resultado de reporte, no un campo opcional posterior.
type Frescura struct {
	// AplicadoHasta es el instante del último evento aplicado a la proyección.
	AplicadoHasta time.Time
	// Stale indica si la proyección quedó atrás de un umbral observable.
	Stale bool
}

// Resultado es la envoltura común: los datos del reporte más su frescura. Los
// datos concretos los tipa cada caso de uso con tipos de pf-pkg-contracts cuando
// se implemente la lectura; aquí se declara la forma compartida.
type Resultado[T any] struct {
	Datos    T
	Frescura Frescura
}
