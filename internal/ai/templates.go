package ai

import (
	"fmt"
	"strings"
)

// Facts are the only values that ever appear in a message. They come from the
// database, never from a model.
type Facts struct {
	Name, Amount, Currency, Platform, DaysLeft, ReplyTo string
}

type template struct{ Subject, Body string }

var templates = map[string]template{
	"UNCLAIMED_SIGNUP": {
		"Your {{platform}} payment of {{amount}} {{currency}} is waiting",
		"Hi {{name}}, we sent you {{amount}} {{currency}} through PayPal, but it is unclaimed because there is no confirmed PayPal account for this email. PayPal has sent you a sign-up link. Please create or confirm your account within {{days_left}} days to receive it. If this email is wrong, reply with the correct one. Thanks, {{platform}}",
	},
	"UNCLAIMED_REMINDER_D20": {
		"Reminder: {{amount}} {{currency}} still unclaimed",
		"Hi {{name}}, your payment of {{amount}} {{currency}} is still unclaimed and will be returned to us in {{days_left}} days. Please claim it through the PayPal link, or reply with a different email. Thanks, {{platform}}",
	},
	"UNCLAIMED_FINAL_D27": {
		"Last chance to claim {{amount}} {{currency}}",
		"Hi {{name}}, your payment will be returned in {{days_left}} days. If you still want it, claim it now or reply with the email of your PayPal account and we will send it again. Thanks, {{platform}}",
	},
	"INVALID_EMAIL": {
		"We could not send your payment",
		"Hi {{name}}, PayPal could not deliver your {{amount}} {{currency}} payment because the email we have appears to be invalid. Please reply with the email address of your PayPal account. We will confirm before sending anything again. Thanks, {{platform}}",
	},
	"ACCOUNT_ISSUE": {
		"Action needed on your PayPal account",
		"Hi {{name}}, PayPal could not complete your {{amount}} {{currency}} payment because of a restriction on the receiving account. We cannot see the details. Please check your PayPal account, or reply with a different PayPal email. Thanks, {{platform}}",
	},
	"CURRENCY_NOT_SUPPORTED": {
		"Currency problem with your payment",
		"Hi {{name}}, your PayPal account cannot receive {{currency}}. Reply with another currency you can receive or a different PayPal account, and we will review it before sending. Thanks, {{platform}}",
	},
	"LIMIT_EXCEEDED": {
		"Your account cannot receive this payment yet",
		"Hi {{name}}, PayPal reports that your account has reached a receiving limit, so your {{amount}} {{currency}} payment is on hold on our side. Please resolve this with PayPal and reply to tell us. Thanks, {{platform}}",
	},
	"COUNTRY_NOT_ALLOWED": {
		"We cannot pay you through PayPal",
		"Hi {{name}}, PayPal does not allow this payout to your country, so we cannot send {{amount}} {{currency}} this way. Our team will contact you at {{reply_to}} about another option. Thanks, {{platform}}",
	},
	"REISSUE_CONFIRMATION": {
		"Your payment has been sent again",
		"Hi {{name}}, we have sent {{amount}} {{currency}} to the PayPal email you gave us. The earlier attempt was cancelled, so you will receive it only once. Thanks, {{platform}}",
	},
	"CANCELLED_NOTICE": {
		"Your unclaimed payment was cancelled",
		"Hi {{name}}, your unclaimed payment of {{amount}} {{currency}} was cancelled and returned to us. Reply with your PayPal email if you still want to be paid. Thanks, {{platform}}",
	},
}

// TemplateKeys lists the available template keys.
func TemplateKeys() []string {
	keys := make([]string, 0, len(templates))
	for k := range templates {
		keys = append(keys, k)
	}
	return keys
}

// Render fills a template with facts.
func Render(key string, f Facts) (subject, body string, err error) {
	t, ok := templates[key]
	if !ok {
		return "", "", fmt.Errorf("unknown template %q", key)
	}
	r := strings.NewReplacer("{{name}}", f.Name, "{{amount}}", f.Amount, "{{currency}}", f.Currency,
		"{{platform}}", f.Platform, "{{days_left}}", f.DaysLeft, "{{reply_to}}", f.ReplyTo)
	return r.Replace(t.Subject), r.Replace(t.Body), nil
}
