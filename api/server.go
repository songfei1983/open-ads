package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

var tracer = otel.Tracer("echo-server")

func main() {
	tp, err := initTracer()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(otelecho.Middleware("open-ads-api", otelecho.WithTracerProvider(tp)))
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	e.POST("/users", saveUser)
	e.GET("/users/:id", getUser)
	e.PUT("/users/:id", updateUser)
	e.DELETE("/users/:id", deleteUser)
	e.Logger.Fatal(e.Start(":8000"))
}

func getUser(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "getUser")
	defer span.End()
	id := c.Param("id")
	Logger(ctx).Info("getUser", zap.String("id", id))
	span.AddEvent("getUser", trace.WithAttributes(attribute.String("id", id)))
	return c.String(http.StatusOK, id)
}

func saveUser(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "saveUser")
	defer span.End()
	Logger(ctx).Info("saveUser")
	span.AddEvent("saveUser")
	return c.NoContent(http.StatusCreated)
}

func updateUser(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "updateUser")
	defer span.End()
	id := c.Param("id")
	Logger(ctx).Info("updateUser", zap.String("id", id))
	span.AddEvent("updateUser", trace.WithAttributes(attribute.String("id", id)))
	return c.String(http.StatusOK, id)
}

func deleteUser(c echo.Context) error {
	ctx, span := tracer.Start(c.Request().Context(), "deleteUser")
	defer span.End()
	id := c.Param("id")
	Logger(ctx).Info("deleteUser", zap.String("id", id))
	span.AddEvent("deleteUser", trace.WithAttributes(attribute.String("id", id)))
	return c.String(http.StatusOK, id)
}

func initTracer() (*sdktrace.TracerProvider, error) {
	exporter, err := newJaegerTraceProvider(context.Background())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp, nil
}

func newJaegerTraceProvider(ctx context.Context) (sdktrace.SpanExporter, error) {
	endpoint := os.Getenv("EXPORTER_ENDPOINT")
	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure())
	return traceExporter, err
}

var (
	once   sync.Once
	logger *otelzap.Logger
)

// Logger ensures that the caller does not forget to pass the context.
func Logger(ctx context.Context) otelzap.LoggerWithCtx {
	once.Do(func() {
		l, err := zap.NewDevelopment()
		if err != nil {
			panic(err)
		}
		logger = otelzap.New(l)
	})
	return logger.Ctx(ctx)
}
