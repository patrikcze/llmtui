package decision

import (
	"fmt"
	"math"
	"sort"
)

const (
	MinCalibrationTemperature = 0.5
	MaxCalibrationTemperature = 5.0
)

type DecodeOptions struct {
	Temperature       float64
	TemperatureByType map[QuestionType]float64
	TemperatureBySize map[string]float64
}

// TemperatureBucket matches the upstream calibration bucket names.
func TemperatureBucket(questionType QuestionType, optionCount int) string {
	var size string
	switch {
	case optionCount <= 2:
		size = "2"
	case optionCount <= 5:
		size = "3-5"
	case optionCount <= 10:
		size = "6-10"
	default:
		size = "11+"
	}
	return fmt.Sprintf("%s:%s", questionType, size)
}

// DecodeLogits turns one marker-logit vector into the normalized public
// answer. It applies a bounded temperature and uses a stable softmax, so a
// backend cannot publish NaN/Inf values into routing or policy inputs.
func DecodeLogits(question Question, logits []float64, options DecodeOptions) (Answer, error) {
	if err := question.validate("decode"); err != nil {
		return Answer{}, err
	}
	labels, err := RenderOptions(question)
	if err != nil {
		return Answer{}, err
	}
	if len(logits) != len(labels) || len(logits) == 0 {
		return Answer{}, fmt.Errorf("%w: got %d logits for %d options", ErrInvalid, len(logits), len(labels))
	}
	for _, logit := range logits {
		if math.IsNaN(logit) || math.IsInf(logit, 0) {
			return Answer{}, fmt.Errorf("%w: logits must be finite", ErrInvalid)
		}
	}
	temperature := options.Temperature
	if temperature == 0 {
		temperature = 1
	}
	if bySize, ok := options.TemperatureBySize[TemperatureBucket(question.Type, len(logits))]; ok {
		temperature = bySize
	} else if byType, ok := options.TemperatureByType[question.Type]; ok {
		temperature = byType
	}
	temperature = clampTemperature(temperature)
	probabilities := softmax(logits, temperature)
	confidence := confidenceFromProbabilities(probabilities)

	answer := Answer{Type: question.Type, Confidence: confidence}
	switch question.Type {
	case QuestionChoice:
		answer.Choice = labels[argmax(probabilities)]
		answer.Probabilities = make(map[string]float64, len(labels))
		for i, label := range choiceLabels(question) {
			answer.Probabilities[label] = probabilities[i]
		}
	case QuestionScore:
		for i, probability := range probabilities {
			answer.Score += float64(i) * probability
		}
		answer.Probabilities = make(map[string]float64, len(probabilities))
		for i, probability := range probabilities {
			answer.Probabilities[fmt.Sprintf("%d", i)] = probability
		}
	case QuestionNoul:
		answer.Probability = probabilities[1]
		answer.Confidence = math.Max(probabilities[1], probabilities[0])
	}
	if err := ValidateAnswer(answer); err != nil {
		return Answer{}, err
	}
	return answer, nil
}

func choiceLabels(question Question) []string {
	switch criteria := question.Criteria.(type) {
	case []string:
		return append([]string(nil), criteria...)
	case map[string]string:
		labels := make([]string, 0, len(criteria))
		for label := range criteria {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		return labels
	case map[string]any:
		labels := make([]string, 0, len(criteria))
		for label := range criteria {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		return labels
	default:
		return nil
	}
}

func clampTemperature(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	if value < MinCalibrationTemperature {
		return MinCalibrationTemperature
	}
	if value > MaxCalibrationTemperature {
		return MaxCalibrationTemperature
	}
	return value
}

func softmax(logits []float64, temperature float64) []float64 {
	maxLogit := logits[0] / temperature
	for _, logit := range logits[1:] {
		if value := logit / temperature; value > maxLogit {
			maxLogit = value
		}
	}
	probabilities := make([]float64, len(logits))
	var total float64
	for i, logit := range logits {
		probabilities[i] = math.Exp(logit/temperature - maxLogit)
		total += probabilities[i]
	}
	for i := range probabilities {
		probabilities[i] /= total
	}
	return probabilities
}

func confidenceFromProbabilities(probabilities []float64) float64 {
	if len(probabilities) < 2 {
		return 1
	}
	var entropy float64
	for _, probability := range probabilities {
		if probability > 0 {
			entropy -= probability * math.Log(probability)
		}
	}
	confidence := 1 - entropy/math.Log(float64(len(probabilities)))
	return math.Min(1, math.Max(0, confidence))
}

func argmax(values []float64) int {
	index := 0
	for i := 1; i < len(values); i++ {
		if values[i] > values[index] {
			index = i
		}
	}
	return index
}
