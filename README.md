# WebRTC Walkie-Talkie CLI
Small walkie-talkie CLI made with webRTC protocol. Works between two peers, so you and another person
can chat via audio over peer-to-peer connection

### Setup
Set up the signaling server. This is an HTTP server that exchanges SDP ([Session Description Protocol](https://webrtchacks.com/sdp-anatomy/))
between two peers.

The server to deploy is in this [repo](https://github.com/jackzhen1996/webrtc-signaling-server). You can deploy in any cloud providers, or you can test it on your local
network. If deployed locally, just use ``http://localhost:8080`` as the URL

```
go build .
./cli-walkie-talkie --name={your name} --signalingUrl={url}
```

### How to use
Press space to start recording audio, press space again to send audio. Like
below:

