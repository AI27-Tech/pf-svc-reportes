package proyecciones

import "time"

// ConRelojParaPrueba ancla el reloj del Lector para pruebas deterministas de la
// marca stale. Exportada desde export_test.go: solo existe durante las pruebas
// del paquete (incluidas las externas proyecciones_test), sin ampliar la
// superficie pública de producción.
func ConRelojParaPrueba(l *Lector, ahora time.Time) {
	l.ahora = func() time.Time { return ahora }
}
