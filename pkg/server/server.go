package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httputil"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/xakep666/ps3netsrv-go/pkg/proto"
)

type Server struct {
	Handler      Handler
	BufferPool   httputil.BufferPool
	Log          *zap.Logger
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func (s *Server) Serve(ln net.Listener) error {
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("accept failed: %w", err)
		}

		go s.serveConn(conn)
	}
}

func (s *Server) setConnWriteDeadline(conn net.Conn) error {
	if s.WriteTimeout == 0 {
		return nil
	}

	return conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
}

func (s *Server) setConnReadDeadline(conn net.Conn) error {
	if s.ReadTimeout == 0 {
		return nil
	}

	return conn.SetReadDeadline(time.Now().Add(s.ReadTimeout))
}

func (s *Server) serveConn(conn net.Conn) {
	ctx := &Context{
		RemoteAddr: conn.RemoteAddr(),
		rd:         proto.Reader{Reader: conn},
		wr:         proto.Writer{Writer: conn, BufferPool: s.BufferPool},
	}
	log := s.Log.With(zap.Stringer("remote", conn.RemoteAddr()))
	log.Info("Client connected")

	defer log.Info("Client disconnected")

	defer func() {
		if err := ctx.Close(); err != nil {
			log.Warn("State closed with errors", zap.Error(err))
		}
	}()

	defer conn.Close()

	for {
		if err := s.setConnReadDeadline(conn); err != nil {
			log.Error("Failed to set read deadline", zap.Error(err))
			return
		}

		opCode, err := ctx.rd.ReadCommand()
		switch {
		case errors.Is(err, nil):
			// pass
		case errors.Is(err, io.EOF):
			log.Info("Connection closed")
			return
		default:
			log.Error("Read command failed", zap.Error(err))
			return
		}

		log := log.With(zap.Stringer("op", opCode))
		log.Debug("Received opcode")

		if err := s.setConnWriteDeadline(conn); err != nil {
			log.Error("Failed to set write deadline", zap.Error(err))
			return
		}

		if err := s.handleCommand(opCode, ctx); err != nil {
			log.Warn("Command handler failed", zap.Error(err))
			return
		}
	}
}

func (s *Server) handleCommand(opCode proto.OpCode, ctx *Context) error {
	switch opCode {
	case proto.CmdOpenDir:
		return s.handleOpenDir(ctx)
	case proto.CmdReadDir:
		return s.handleReadDir(ctx)
	case proto.CmdStatFile:
		return s.handleStatFile(ctx)
	case proto.CmdOpenFile:
		return s.handleOpenFile(ctx)
	case proto.CmdReadFile:
		return s.handleReadFile(ctx)
	case proto.CmdReadFileCritical:
		return s.handleReadFileCritical(ctx)
	case proto.CmdReadDirEntry:
		return s.handleReadDirEntry(ctx)
	default:
		return fmt.Errorf("unknown opCode: %d", opCode)
	}
}

func (s *Server) handleOpenDir(ctx *Context) error {
	// here we should check that we can read requested dir and set state if it's true
	dirPath, err := ctx.rd.ReadOpenDir()
	if err != nil {
		return fmt.Errorf("read dir failed: %w", err)
	}

	return ctx.wr.SendOpenDirResult(s.Handler.HandleOpenDir(ctx, dirPath))
}

func (s *Server) handleReadDirEntry(ctx *Context) error {
	return ctx.wr.SendReadDirEntryResult(s.Handler.HandleReadDirEntry(ctx))
}

func (s *Server) handleReadDir(ctx *Context) error {
	return ctx.wr.SendReadDirResult(s.Handler.HandleReadDir(ctx))
}

func (s *Server) handleStatFile(ctx *Context) error {
	filePath, err := ctx.rd.ReadStatFile()
	if err != nil {
		return fmt.Errorf("read stat path failed: %w", err)
	}

	fi, err := s.Handler.HandleStatFile(ctx, filePath)
	if err != nil {
		return ctx.wr.SendStatFileError()
	}

	return ctx.wr.SendStatFileResult(fi)
}

func (s *Server) handleOpenFile(ctx *Context) error {
	// Here can be some special paths:
	// * CLOSEFILE (in original code it's just send success with closing already opened file if present)

	filePath, err := ctx.rd.ReadOpenFile()
	if err != nil {
		return fmt.Errorf("read file to open path failed: %w", err)
	}

	filePath = filepath.Clean(filePath)

	if _, name := filepath.Split(filePath); name == "CLOSEFILE" {
		s.Handler.HandleCloseFile(ctx)
		return ctx.wr.SendOpenFileForCLOSEFILE()
	}

	if err := s.Handler.HandleOpenFile(ctx, filePath); err != nil {
		return ctx.wr.SendOpenFileError()
	}

	return ctx.wr.SendOpenFileResult(ctx.State.ROFile)
}

func (s *Server) handleReadFile(ctx *Context) error {
	toRead, off, err := ctx.rd.ReadReadFile()
	if err != nil {
		return fmt.Errorf("read read file params failed: %w", err)
	}

	return ctx.wr.SendReadFileResult(s.Handler.HandleReadFile(ctx, toRead, off))
}

func (s *Server) handleReadFileCritical(ctx *Context) error {
	toRead, off, err := ctx.rd.ReadReadFileCritical()
	if err != nil {
		return fmt.Errorf("read read file critical params failed: %w", err)
	}

	rd, err := s.Handler.HandleReadFileCritical(ctx, toRead, off)
	if err != nil {
		return fmt.Errorf("read file critical failed: %w", err)
	}

	return ctx.wr.SendReadFileCriticalResult(rd)
}
