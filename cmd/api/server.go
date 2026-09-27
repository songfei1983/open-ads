package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/labstack/echo-contrib/echoprometheus"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(echoprometheus.NewMiddleware("echo"))    // adds middleware to gather metrics
	e.GET("/metrics", echoprometheus.NewHandler()) // adds route to serve gathered metrics
	e.GET("/users", listUser)
	e.GET("/users/:id", getUser)
	e.Server.Handler = otelhttp.NewHandler(e, "open-ads-api")
	e.Logger.Fatal(e.Start(":80"))
}

func getUser(c echo.Context) error {
	_, span := tracer.Start(c.Request().Context(), "getUser")
	defer span.End()
	id := c.Param("id")
	span.AddEvent("getUser", trace.WithAttributes(attribute.String("id", id)))
	return c.String(http.StatusOK, id)
}

func listUser(c echo.Context) error {
	_, span := tracer.Start(c.Request().Context(), "saveUser")
	defer span.End()
	span.AddEvent("listUser")
	return c.NoContent(http.StatusCreated)
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
