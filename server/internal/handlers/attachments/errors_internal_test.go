package attachments

import (
	"net/http"
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestErrorMapperCoversEverySentinel(t *testing.T) {
	t.Parallel()

	h := &Handler{HandlerBase: base.NewHandlerBase(&config.Config{})}

	testsupport.AssertErrorMapping(t, h.writePutError, []testsupport.ErrorCase{
		testsupport.Case(ErrEmptyAttachment, 400),
		testsupport.Case(ErrAttachmentTooLarge, 413),
		testsupport.Case(ErrUnsupportedMediaType, 415),
		testsupport.Case(http.ErrAbortHandler, http.StatusInternalServerError),
	})
}
