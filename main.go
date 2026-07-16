package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
)

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}
type multiError interface {
	error
	Unwrap() []error
}
type closeFunc func() error

var logger *slog.Logger

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger, closeFunc, err := initializeLogger()
	// closeFuncWrapper := func() {
	// 	logger.Info(closeFunc().Error())
	// 	closeFunc()
	// }
	// defer closeFuncWrapper()

	defer func() {
		if err := closeFunc(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close logger: %v\n", err)
		}
	}()

	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
	// }
	st, err := store.New(dataDir, logger)
	if err != nil {
		// logger.Error(fmt.Sprintf("failed to create store: %v", err))
		logger.Error("failed to create store", slog.Any("error", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()
	// logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", httpPort))
	logger.Debug("Linkos is running", slog.String("baseUrl", "http://localhost"), slog.Int("port", httpPort))
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger.Debug("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		// logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		logger.Error("failed ot shutdown server", slog.Any("error", err))
		return 1
	}
	if serverErr != nil {
		// logger.Error(fmt.Sprintf("server error: %v", serverErr))
		logger.Error("server error", slog.Any("error", err))
		return 1
	}
	return 0
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: replaceAttr})
	logFilePath := os.Getenv("LINKO_LOG_FILE")
	flushfunc := func() error { return nil }
	if len(logFilePath) > 0 {
		logFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return nil, flushfunc, fmt.Errorf("failed to open log file: %v", err)
		}
		infoHandler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: replaceAttr})
		// multiwriter := bufio.NewWriterSize(io.MultiWriter(os.Stderr, logFile), 8192)
		flushfunc = func() error {
			// err := multiwriter.Flush()
			// if err != nil {
			// 	return err
			// }
			// err = logFile.Close()
			// if err != nil {
			// 	return err
			// }
			return nil
		}

		return slog.New(slog.NewMultiHandler(debugHandler, infoHandler)), flushfunc, nil
		// return slog.New(slog.NewTextHandler(bufio.NewWriterSize(multiwriter, 8192), nil)), flushfunc, nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil)), flushfunc, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		if multiErr, ok := errors.AsType[multiError](err); ok {
			errs := multiErr.Unwrap()
			var attrgroups []slog.Attr
			for i, e := range errs {
				attr := linkoerr.Attrs(e)
				// attr = append(attr, slog.Attr{Key: fmt.Sprintf("%d", i), Value: slog.StringValue(fmt.Sprintf("%+v", e))})
				attr = append(attr, slog.Attr{
					Key:   "message",
					Value: slog.StringValue(e.Error()),
				})
				// attr = append(attr, slog.Attr{
				// 	Key:   "stack_trace",
				// 	Value: slog.StringValue(fmt.Sprintf("%+v", e.StackTrace())),
				// })
				group := slog.Attr{
					Key:   fmt.Sprintf("error_%d", i+1),
					Value: slog.AnyValue(attr),
				}
				attrgroups = append(attrgroups, group)
			}
			return slog.GroupAttrs("errors", attrgroups...)
		}
		if stackErr, ok := errors.AsType[stackTracer](err); ok {
			attr := linkoerr.Attrs(err)
			attr = append(attr, slog.Attr{
				Key:   "message",
				Value: slog.StringValue(stackErr.Error()),
			})
			attr = append(attr, slog.Attr{
				Key:   "stack_trace",
				Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
			})
			return slog.GroupAttrs("error", attr...)

			// return slog.GroupAttrs("error", attr...)
			// return slog.GroupAttrs("error", slog.Attr{
			// 	Key:   "message",
			// 	Value: slog.StringValue(stackErr.Error()),
			// }, slog.Attr{
			// 	Key:   "stack_trace",
			// 	Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
			// })
			// return slog.GroupAttrs("error", attr...)
		}
	}
	return a
}
