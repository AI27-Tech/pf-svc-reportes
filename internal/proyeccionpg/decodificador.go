// Package proyeccionpg implementa, contra el pool de svc_reportes, los puertos
// que consume ConsumidorProyecciones: Proyeccion, Inbox y DLQ (internal/
// consumidores). Es el adaptador real de las proyecciones del lado escritura;
// el paquete internal/proyecciones es el lado lectura y no se toca aquí.
//
// Alcance (E6-T1): se proyectan los 4 tipos viaje.* que operacion publica hoy
// (creado, estado_cambiado, asignado, reasignado). viaje.cerrado,
// condicion_abierta y condicion_cerrada quedan registrados en el consumidor pero
// sin productor, así que no se proyectan todavía (no son venenosos; simplemente
// no llegan).
package proyeccionpg

import (
	"encoding/json"
	"fmt"

	"github.com/AI27-Tech/pf-pkg-platform-go/bus"
	"github.com/AI27-Tech/pf-svc-reportes/internal/consumidores"
)

// Decodificar traduce el Sobre del bus a la vista Evento que el consumidor usa.
// La identidad (evento_id, tipo, versión, agregado) se copia 1:1; el Payload
// JSON se deja crudo (json.RawMessage) y lo decodifica el adaptador por tipo, en
// la misma forma de map que el productor serializó (operacion arma el payload a
// mano, no desde el struct generado del proto; sus claves mandan).
func Decodificar(s bus.Sobre) consumidores.Evento {
	return consumidores.Evento{
		EventoID:   s.EventoID,
		Tipo:       s.Tipo,
		Version:    s.Version,
		AgregadoID: s.AgregadoID,
		Payload:    json.RawMessage(s.Payload),
	}
}

// payloadDe extrae el JSON crudo del Evento. El consumidor transporta el payload
// como `any`; el decodificador siempre pone json.RawMessage, así que un valor de
// otro tipo es un error de programación del cableado, no un dato externo.
func payloadDe(e consumidores.Evento) (json.RawMessage, error) {
	raw, ok := e.Payload.(json.RawMessage)
	if !ok {
		return nil, fmt.Errorf("proyeccionpg: payload de %s no es json.RawMessage (es %T)", e.Tipo, e.Payload)
	}
	return raw, nil
}
