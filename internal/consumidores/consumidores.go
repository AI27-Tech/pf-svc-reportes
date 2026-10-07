// Package consumidores aloja los consumidores durables de reportes (ADR 29). El
// consumidor de proyecciones aplica el cambio de proyección y registra el
// evento_id en inbox_evento dentro de la MISMA transacción; el acuse al broker
// ocurre después (al menos una vez, reportes.md (g), E6-T2). Un evento duplicado
// no produce un segundo efecto: reaplicar viaje.estado_cambiado duplicaría una
// duración, y eso es justo lo que inbox_evento impide.
//
// Orden por agregado (clave de orden = viaje_id para viaje.*; ADR 29 §2), sin
// clave global. reportes es consumidor TERMINAL: no publica, no tiene outbox.
//
// Evento venenoso (tipo/versión no registrada o reintentos agotados) → DLQ/
// cuarentena propia de reportes, con solo metadatos sanitizados en logs/métricas
// (ADR 29 §4); la suscripción avanza solo tras confirmar la cuarentena. Los
// parámetros operativos (reintentos, backoff, retención, plazo de reentrega,
// alarmas) quedan configurables y pendientes; no se fijan números.
//
// Scaffolding: la mecánica de idempotencia y de DLQ se modela aquí sobre un
// puerto de proyección e inbox para poder probarla sin Postgres; el adaptador
// real contra el pool de svc_reportes se cablea después (TODO). El punto de
// extensión de reconstrucción (ADR 30) queda declarado y sin implementar (R12).
package consumidores

import (
	"context"
	"errors"
	"fmt"
)

// ErrEventoVenenoso marca un evento cuyo tipo/versión no está registrado o que
// agotó sus reintentos: debe ir a la DLQ/cuarentena, no reintentarse en vivo.
var ErrEventoVenenoso = errors.New("evento venenoso: tipo/versión no registrada")

// Evento es la vista mínima que el consumidor necesita del Sobre (P13). Los
// tipos ricos del dominio llegan en Payload; el scaffold solo usa la identidad y
// el tipo para demostrar la idempotencia y el enrutamiento a DLQ.
type Evento struct {
	EventoID   string // dedup key del Sobre (evento_id)
	Tipo       string // p. ej. "viaje.estado_cambiado"
	Version    int
	AgregadoID string // viaje_id para viaje.* (clave de orden)
	Payload    any
}

// Proyeccion aplica el efecto de un evento ya deduplicado. La implementación
// real escribe en hecho_* usando el pool de svc_reportes; aquí es un puerto para
// poder probar la idempotencia sin DB.
type Proyeccion interface {
	Aplicar(ctx context.Context, e Evento) error
}

// Inbox registra y consulta evento_id procesados (inbox_evento). En producción
// el registro y el Aplicar de la proyección ocurren en la MISMA transacción;
// el puerto lo refleja con Procesar, que debe ser atómico respecto de Aplicar.
type Inbox interface {
	// YaProcesado indica si el evento_id ya está en inbox_evento.
	YaProcesado(ctx context.Context, eventoID string) (bool, error)
	// Marcar registra el evento_id como procesado (en la misma tx que la proyección).
	Marcar(ctx context.Context, eventoID, tipo, consumidor string) error
}

// DLQ recibe los eventos venenosos con metadatos sanitizados (sin payload).
type DLQ interface {
	Cuarentena(ctx context.Context, eventoID, tipo string, motivo string) error
}

// tiposRegistrados son las familias de evento que reportes consume en V1 (R7).
// Un evento fuera de este conjunto es venenoso (va a DLQ), no un error del
// consumidor. catalogo.actualizado y la entrega de mensajeria NO están aquí:
// están bloqueados por R8 y R9.
var tiposRegistrados = map[string]struct{}{
	"viaje.creado":            {},
	"viaje.estado_cambiado":   {},
	"viaje.asignado":          {},
	"viaje.reasignado":        {},
	"viaje.condicion_abierta": {},
	"viaje.condicion_cerrada": {},
	"viaje.cerrado":           {},
}

// ConsumidorProyecciones aplica eventos a las proyecciones con idempotencia por
// evento_id y enruta los venenosos a la DLQ.
type ConsumidorProyecciones struct {
	proy  Proyeccion
	inbox Inbox
	dlq   DLQ
}

// NewConsumidorProyecciones construye el consumidor con sus puertos resueltos.
func NewConsumidorProyecciones(proy Proyeccion, inbox Inbox, dlq DLQ) *ConsumidorProyecciones {
	return &ConsumidorProyecciones{proy: proy, inbox: inbox, dlq: dlq}
}

// Procesar maneja un evento entrante: si el tipo no está registrado lo manda a
// cuarentena; si ya se procesó, es un no-op idempotente; si es nuevo, lo aplica
// y lo marca. En producción Aplicar + Marcar van en la misma transacción; el
// orden aquí (aplicar, luego marcar) refleja esa unidad atómica.
func (c *ConsumidorProyecciones) Procesar(ctx context.Context, e Evento) error {
	if _, ok := tiposRegistrados[e.Tipo]; !ok {
		if err := c.dlq.Cuarentena(ctx, e.EventoID, e.Tipo, "tipo no registrado"); err != nil {
			return fmt.Errorf("cuarentena de %s: %w", e.Tipo, err)
		}
		return ErrEventoVenenoso
	}

	ya, err := c.inbox.YaProcesado(ctx, e.EventoID)
	if err != nil {
		return fmt.Errorf("consultar inbox: %w", err)
	}
	if ya {
		return nil // duplicado: no-op idempotente (no reaplica la duración)
	}

	// En producción, estas dos operaciones son UNA transacción contra el pool de
	// svc_reportes: si Marcar falla, Aplicar se revierte y el evento se reintenta.
	if err := c.proy.Aplicar(ctx, e); err != nil {
		return fmt.Errorf("aplicar proyección de %s: %w", e.Tipo, err)
	}
	if err := c.inbox.Marcar(ctx, e.EventoID, e.Tipo, "proyecciones"); err != nil {
		return fmt.Errorf("marcar inbox de %s: %w", e.Tipo, err)
	}
	return nil
}

// --- Punto de extensión de reconstrucción (ADR 30) — SIN IMPLEMENTAR (R12) ---
//
// Las representaciones de reconstrucción (fuente, familia, versión, modo,
// corrida/elemento, rango, marca de agua, consumidor, idempotencia, conteos/
// checksum) NO existen en el contrato todavía (R12). La fuente canónica es el
// productor: operacion (E1-T9) para viaje/transiciones/asignaciones/condiciones,
// catalogos (E5-T5) para versiones y vigencias. Una bandera es_relectura: true
// no basta (ADR 30 §4–5).
//
// TODO(reportes): consumir reconstrucción cuando R12 exista, reutilizando la
// misma idempotencia por evento_id de arriba.
