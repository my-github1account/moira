package slack

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	slackdown "github.com/karriereat/blackfriday-slack"
	"github.com/moira-alert/moira"
	"github.com/moira-alert/moira/senders"
	blackfriday "github.com/russross/blackfriday/v2"

	"github.com/slack-go/slack"
)

const (
	okEmoji        = ":moira-state-ok:"
	warnEmoji      = ":moira-state-warn:"
	errorEmoji     = ":moira-state-error:"
	nodataEmoji    = ":moira-state-nodata:"
	exceptionEmoji = ":moira-state-exception:"
	testEmoji      = ":moira-state-test:"

	messageMaxCharacters = 4000

	//see errors https://api.slack.com/methods/chat.postMessage
	ErrorTextChannelArchived = "is_archived"
	ErrorTextChannelNotFound = "channel_not_found"
	ErrorTextNotInChannel    = "not_in_channel"
)

var stateEmoji = map[moira.State]string{
	moira.StateOK:        okEmoji,
	moira.StateWARN:      warnEmoji,
	moira.StateERROR:     errorEmoji,
	moira.StateNODATA:    nodataEmoji,
	moira.StateEXCEPTION: exceptionEmoji,
	moira.StateTEST:      testEmoji,
}

// Sender implements moira sender interface via slack
type Sender struct {
	frontURI string
	useEmoji bool
	logger   moira.Logger
	location *time.Location
	client   *slack.Client
}

// Init read yaml config
func (sender *Sender) Init(senderSettings map[string]string, logger moira.Logger, location *time.Location, dateTimeFormat string) error {
	apiToken := senderSettings["api_token"]
	if apiToken == "" {
		return fmt.Errorf("can not read slack api_token from config")
	}
	sender.useEmoji, _ = strconv.ParseBool(senderSettings["use_emoji"])
	sender.logger = logger
	sender.frontURI = senderSettings["front_uri"]
	sender.location = location
	sender.client = slack.New(apiToken)
	return nil
}

// SendEvents implements Sender interface Send
func (sender *Sender) SendEvents(events moira.NotificationEvents, contact moira.ContactData, trigger moira.TriggerData, plots [][]byte, throttled bool) error {
	message := sender.buildMessage(events, trigger, throttled)
	useDirectMessaging := useDirectMessaging(contact.Value)
	emoji := sender.getStateEmoji(events.GetSubjectState())
	channelID, threadTimestamp, err := sender.sendMessage(message, contact.Value, trigger.ID, useDirectMessaging, emoji)
	if err != nil {
		return err
	}
	if channelID != "" && len(plots) > 0 {
		err = sender.sendPlots(plots, channelID, threadTimestamp, trigger.ID)
		if err != nil {
			sender.logger.Warning().
				String("trigger_id", trigger.ID).
				String("contact_value", contact.Value).
				String("contact_type", contact.Type).
				Error(err)
		}
	}
	return nil
}

func (sender *Sender) buildMessage(events moira.NotificationEvents, trigger moira.TriggerData, throttled bool) string {
	var message strings.Builder

	title := sender.buildTitle(events, trigger)
	titleLen := len([]rune(title))

	desc := sender.buildDescription(trigger)
	descLen := len([]rune(desc))

	eventsString := sender.buildEventsString(events, -1, throttled)
	eventsStringLen := len([]rune(eventsString))

	charsLeftAfterTitle := messageMaxCharacters - titleLen

	descNewLen, eventsNewLen := senders.CalculateMessagePartsLength(charsLeftAfterTitle, descLen, eventsStringLen)

	if descLen != descNewLen {
		desc = desc[:descNewLen] + "...\n"
	}
	if eventsNewLen != eventsStringLen {
		eventsString = sender.buildEventsString(events, eventsNewLen, throttled)
	}

	message.WriteString(title)
	message.WriteString(desc)
	message.WriteString(eventsString)
	return message.String()
}

func (sender *Sender) buildDescription(trigger moira.TriggerData) string {
	desc := trigger.Desc
	if trigger.Desc != "" {
		desc = string(blackfriday.Run([]byte(desc), blackfriday.WithRenderer(&slackdown.Renderer{})))
		desc += "\n"
	}
	return desc
}

func (sender *Sender) buildTitle(events moira.NotificationEvents, trigger moira.TriggerData) string {
	title := fmt.Sprintf("*%s*", events.GetSubjectState())
	triggerURI := trigger.GetTriggerURI(sender.frontURI)
	if triggerURI != "" {
		title += fmt.Sprintf(" <%s|%s>", triggerURI, trigger.Name)
	} else if trigger.Name != "" {
		title += " " + trigger.Name
	}

	tags := trigger.GetTags()
	if tags != "" {
		title += " " + tags
	}

	title += "\n"
	return title
}

// buildEventsString builds the string from moira events and limits it to charsForEvents.
// if n is negative buildEventsString does not limit the events string
func (sender *Sender) buildEventsString(events moira.NotificationEvents, charsForEvents int, throttled bool) string {
	charsForThrottleMsg := 0
	throttleMsg := "\nPlease, *fix your system or tune this trigger* to generate less events."
	if throttled {
		charsForThrottleMsg = len([]rune(throttleMsg))
	}
	charsLeftForEvents := charsForEvents - charsForThrottleMsg

	var eventsString string
	eventsString += "```"
	var tailString string

	eventsLenLimitReached := false
	eventsPrinted := 0
	for _, event := range events {
		line := fmt.Sprintf("\n%s: %s = %s (%s to %s)", event.FormatTimestamp(sender.location), event.Metric, event.GetMetricsValues(), event.OldState, event.State)
		if msg := event.CreateMessage(sender.location); len(msg) > 0 {
			line += fmt.Sprintf(". %s", msg)
		}

		tailString = fmt.Sprintf("\n...and %d more events.", len(events)-eventsPrinted)
		tailStringLen := len([]rune("```")) + len([]rune(tailString))
		if !(charsForEvents < 0) && (len([]rune(eventsString))+len([]rune(line)) > charsLeftForEvents-tailStringLen) {
			eventsLenLimitReached = true
			break
		}

		eventsString += line
		eventsPrinted++
	}
	eventsString += "```"

	if eventsLenLimitReached {
		eventsString += tailString
	}

	if throttled {
		eventsString += throttleMsg
	}

	return eventsString
}

func (sender *Sender) sendMessage(message string, contact string, triggerID string, useDirectMessaging bool, emoji string) (string, string, error) {
	params := slack.PostMessageParameters{
		Username:  "Moira",
		AsUser:    useDirectMessaging,
		IconEmoji: emoji,
		Markdown:  true,
		LinkNames: 1,
	}
	sender.logger.Debug().
		String("message", message).
		Msg("Calling slack")

	channelID, threadTimestamp, err := sender.client.PostMessage(contact, slack.MsgOptionText(message, false), slack.MsgOptionPostMessageParameters(params))
	if err != nil {
		errorText := err.Error()
		if errorText == ErrorTextChannelArchived || errorText == ErrorTextNotInChannel ||
			errorText == ErrorTextChannelNotFound {
			return channelID, threadTimestamp, moira.NewSenderBrokenContactError(err)
		}
		return channelID, threadTimestamp, fmt.Errorf("failed to send %s event message to slack [%s]: %s",
			triggerID, contact, errorText)
	}
	return channelID, threadTimestamp, nil
}

func (sender *Sender) sendPlots(plots [][]byte, channelID, threadTimestamp, triggerID string) error {
	filename := fmt.Sprintf("%s.png", triggerID)
	for _, plot := range plots {
		reader := bytes.NewReader(plot)
		uploadParameters := slack.UploadFileV2Parameters{
			FileSize:        len(plot),
			Reader:          reader,
			Title:           filename,
			Filename:        filename,
			Channel:         channelID,
			ThreadTimestamp: threadTimestamp,
		}

		_, err := sender.client.UploadFileV2(uploadParameters)
		if err != nil {
			return err
		}
	}

	return nil
}

// getStateEmoji returns corresponding state emoji
func (sender *Sender) getStateEmoji(subjectState moira.State) string {
	if sender.useEmoji {
		if emoji, ok := stateEmoji[subjectState]; ok {
			return emoji
		}
	}
	return slack.DEFAULT_MESSAGE_ICON_EMOJI
}

// useDirectMessaging returns true if user contact is provided
func useDirectMessaging(contactValue string) bool {
	return len(contactValue) > 0 && contactValue[0:1] == "@"
}
