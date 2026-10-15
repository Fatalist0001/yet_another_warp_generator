package vpn

import (
	"io"
	"net"
	"time"
)

var _ net.Conn = (*idleConn)(nil)

// idleConn — соединение с таймаутом простоя: дедлайн обновляется перед каждым
// чтением и записью, поэтому долгая, но активная передача не рвётся, а мёртвое
// соединение закрывается.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func withIdle(c net.Conn, timeout time.Duration) net.Conn {
	if timeout <= 0 {
		return c
	}
	return &idleConn{Conn: c, timeout: timeout}
}

func (c *idleConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

// CloseWrite / CloseRead пробрасывают полузакрытие, если соединение его умеет.
func (c *idleConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *idleConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// halfClose закрывает сторону записи: получатель должен увидеть EOF, но ещё
// может ответить (полузакрытие TCP).
func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// pump дублирует данные в обе стороны до встречного EOF и закрывает сторону
// записи второго соединения (полузакрытие). Таймаут простоя гарантирует, что
// горутины не зависнут на мёртвом соединении.
func pump(a, b net.Conn, idle time.Duration) {
	ia, ib := withIdle(a, idle), withIdle(b, idle)

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(ia, ib)
		halfClose(ia)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(ib, ia)
		halfClose(ib)
		done <- struct{}{}
	}()
	<-done
	<-done
}
