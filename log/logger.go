package log

import (
	"github.com/fatih/color"
	"go.uber.org/zap"
	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
	"strings"
	"time"
)

var ColorResetStr = "\x1b[0m"
var LenColorResetStr = len(ColorResetStr)

var (
	Red    = color.New(color.FgHiRed).SprintFunc()
	Blue   = color.New(color.FgHiBlue).SprintFunc()
	Yellow = color.New(color.FgHiYellow).SprintFunc()
	Green  = color.New(color.FgHiGreen).SprintFunc()
)

type CliLogger struct {
	*zap.Logger
}

var GlobalLogger *zap.Logger

func Logger() *zap.Logger {
	if GlobalLogger == nil {
		logger, _ := NewConsoleLogger()
		GlobalLogger = logger
		logger.Info("日志初始化成功")
	}
	return GlobalLogger
}

func NewCliLogger() *CliLogger {
	logger, _ := NewConsoleLogger()
	return &CliLogger{logger}
}

func NewConsoleLogger() (*zap.Logger, error) {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:          "T",
		LevelKey:         "L",
		NameKey:          "N",
		CallerKey:        "C",
		MessageKey:       "M",
		StacktraceKey:    "S",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeLevel:      customLevelColorEncoder,
		EncodeTime:       customTimeEncoder,
		EncodeDuration:   zapcore.StringDurationEncoder,
		EncodeCaller:     zapcore.FullCallerEncoder,
		ConsoleSeparator: " ",
	}

	core := zapcore.NewCore(
		&CustomEncoder{zapcore.NewConsoleEncoder(encoderConfig)},
		zapcore.AddSync(color.Output),
		zapcore.DebugLevel,
	)
	return zap.New(core, zap.AddCaller()), nil

}

type CustomEncoder struct {
	zapcore.Encoder
}

func (c *CustomEncoder) EncodeEntry(entry zapcore.Entry, fields []zap.Field) (*buffer.Buffer, error) {
	buf, err := c.Encoder.EncodeEntry(entry, fields)
	if err != nil {
		return buf, err
	}

	switch entry.Level {
	case zapcore.WarnLevel:
		e := buf.String()[:strings.LastIndex(buf.String(), ColorResetStr)+LenColorResetStr]
		t := buf.String()[strings.LastIndex(buf.String(), ColorResetStr)+LenColorResetStr:]
		buf.Reset()
		buf.WriteString(e + Yellow(t))
	case zapcore.ErrorLevel:
		e := buf.String()[:strings.LastIndex(buf.String(), ColorResetStr)+LenColorResetStr]
		t := buf.String()[strings.LastIndex(buf.String(), ColorResetStr)+LenColorResetStr:]
		buf.Reset()
		buf.WriteString(e + Red(t))
	}
	return buf, nil
}
func customLevelColorEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	var colorize func(a ...interface{}) string
	color.NoColor = false

	switch level {
	case zapcore.DebugLevel:
		colorize = Blue
	case zapcore.InfoLevel:
		colorize = Green
	case zapcore.WarnLevel:
		colorize = Yellow
	case zapcore.ErrorLevel:
		colorize = Red
	default:
		colorize = color.New(color.FgHiWhite).SprintFunc()
	}

	enc.AppendString(colorize(level.CapitalString()))
}

// 自定义时间编码器，添加颜色
func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(color.New(color.FgCyan).SprintFunc()(t.Format("2006-01-02 15:04:05.000")))
}
