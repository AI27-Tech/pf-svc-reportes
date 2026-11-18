// Package alcance es el alcance del monitoreo sobre el dashboard de reportes
// (DEC-149, reportes.yaml de tracer.42): qué rol interno lee reportes y con qué
// alcance, y la proyección local de `alcance.cambiado` de identidad (sujeto →
// clientes efectivos) con la que se resuelve en servidor (DEC-053 #3).
//
// La regla de rol (regla.go) es pura. El filtro de viajes (cliente del viaje
// entre los clientes efectivos y paso por APROBADO) va en SQL, en
// internal/proyecciones, para no filtrar en memoria filas, totales ni conteos.
// La proyección (proyeccion.go) vive en Postgres, en el esquema reportes: el
// consumidor de alcance.cambiado es durable y comparte grupo entre réplicas, así
// que una proyección en memoria solo la vería actualizada la réplica que recibió
// el evento.
package alcance

import "github.com/AI27-Tech/pf-pkg-contracts/packages/api-client-go/roles"

// Ambito es lo que un rol interno ve del dashboard de reportes.
type Ambito int

const (
	// Prohibido es 403: el rol no tiene el dashboard (DEC-149 #8, DEC-022 #9).
	Prohibido Ambito = iota
	// Todos es el alcance global (administrador).
	Todos
	// PorClientes son los viajes de los clientes efectivos del sujeto que
	// pasaron por APROBADO (DEC-149 #5, #6, #18).
	PorClientes
)

// Para devuelve el ámbito de un rol INTERNO sobre el dashboard. La tabla de
// DEC-149 #8 ("Dashboard de monitoreo, evidencia y expediente") da el dashboard
// a monitorista (su alcance) y administrador; reportes.yaml de tracer.42 extiende
// el alcance a supervisor y coordinador y da 403 a cualquier otro rol, incluido
// jefe_operativo. Un rol desconocido es Prohibido.
func Para(rol string) Ambito {
	switch roles.Rol(rol) {
	case roles.RolAdministrador:
		return Todos
	case roles.RolMonitorista, roles.RolSupervisor, roles.RolCoordinador:
		return PorClientes
	}
	return Prohibido
}
