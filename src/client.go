package ircdiscord

import (
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/diamondburned/arikawa/discord"
	"github.com/tadeokondrak/ircdiscord/src/session"
	"gopkg.in/sorcix/irc.v2"
)

type Client struct {
	conn               net.Conn
	irc                *irc.Conn
	session            *session.Session
	guild              *discord.Guild
	subscribedChannels map[discord.Snowflake]string
	serverPrefix       irc.Prefix
	clientPrefix       irc.Prefix
	lastMessageID      discord.Snowflake
}

func NewClient(conn net.Conn) *Client {
	return &Client{
		conn:               conn,
		irc:                irc.NewConn(conn),
		subscribedChannels: make(map[discord.Snowflake]string),
	}
}

func (c *Client) Close() error {
	if c.session != nil {
		c.session.Unref()
	}
	return c.irc.Close()
}

func (c *Client) Run() error {
	defer c.Close()

	c.serverPrefix.Name = c.conn.LocalAddr().String()
	c.clientPrefix.Name = c.conn.RemoteAddr().String()

	log.Printf("connected: %v", c.clientPrefix.Name)
	defer log.Printf("disconnected: %v", c.clientPrefix.Name)

initial_loop:
	for {
		msg, err := c.irc.Decode()
		if err != nil {
			return err
		}
		switch msg.Command {
		case irc.CAP, irc.NICK, irc.USER:
			// intentionally left blank
		case irc.PASS:
			if len(msg.Params) != 1 {
				return fmt.Errorf("invalid parameter count for PASS")
			}
			args := strings.SplitN(msg.Params[0], ":", 2)
			session, err := session.Get(args[0])
			if err != nil {
				return err
			}
			c.session = session
			if len(args) > 1 {
				snowflake, err := discord.ParseSnowflake(args[1])
				if err != nil {
					return err
				}
				guild, err := c.session.Guild(snowflake)
				if err != nil {
					return err
				}
				c.guild = guild
			}
			break initial_loop
		default:
			return fmt.Errorf("invalid command received for auth stage: %v",
				msg.Command)
		}
	}

	me, err := c.session.Me()
	if err != nil {
		return err
	}

	c.clientPrefix = irc.Prefix{
		User: me.Username,
		Name: me.Username,
		Host: me.ID.String(),
	}

	c.irc.Encode(&irc.Message{
		Prefix:  &c.serverPrefix,
		Command: irc.RPL_WELCOME,
		Params: []string{c.clientPrefix.Name, fmt.Sprintf("Welcome to IRCdiscord, %s#%s",
			me.Username, me.Discriminator)},
	})

	msgs := make(chan *irc.Message)
	errors := make(chan error)
	go func() {
		for {
			msg, err := c.irc.Decode()
			if err != nil {
				errors <- err
				return
			}
			msgs <- msg
		}
	}()

	events, cancel := c.session.ChanFor(func(e interface{}) bool { return true })
	defer cancel()

	for {
		select {
		case msg := <-msgs:
			if err := c.handleIRCMessage(msg); err != nil {
				return err
			}
		case event := <-events:
			if err := c.handleDiscordEvent(event); err != nil {
				return err
			}
		case err := <-errors:
			return err
		}
	}
}