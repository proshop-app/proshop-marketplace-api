package searchapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// request не повторяет мутации транспортом: ответ может потеряться уже после
// записи. Outbox повторит версионированную пачку, а rebuild сверит поколение.
func (x *Index) request(ctx context.Context, method, path string, payload, out any) (int, error) {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)

		if err != nil {
			return 0, ErrInvalid.Wrap(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, x.base+path, body)

	if err != nil {
		return 0, ErrUnavailable.Wrap(err)
	}
	request.Header.Set("X-Admin-Id", x.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := x.client.Do(request)

	if err != nil {
		return 0, ErrUnavailable.Wrap(err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Не прокидываем тело чужой ошибки и служебные заголовки в публичный API.
		message := fmt.Sprintf("поиск ответил HTTP %d", response.StatusCode)

		return response.StatusCode, ErrUnavailable.WithMessage(message)
	}

	if out == nil {
		if response.StatusCode != http.StatusNoContent {
			return response.StatusCode, ErrUnavailable.WithMessage("неподтверждённая операция поиска")
		}

		return response.StatusCode, nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))

	if err != nil || len(bodyBytes) > responseLimit {
		return response.StatusCode, ErrUnavailable.WithMessage("некорректный размер ответа поиска")
	}

	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return response.StatusCode, ErrUnavailable.WithMessage("некорректный ответ поиска")
	}

	return response.StatusCode, nil
}
