#include "utils/http_client.h"
#include "utils/string_utils.h"
#include <circle/net/dnsclient.h>
#include <circle/net/socket.h>
#include <circle/net/tls.h>
#include <cstring>
#include <sstream>

namespace mm {

HttpClient::HttpClient(CNetSubSystem* net, CScheduler* scheduler)
    : m_pNet(net),
      m_pScheduler(scheduler),
      m_timeout(30000)
{
}

HttpClient::~HttpClient()
{
}

bool HttpClient::ParseUrl(const std::string& url, std::string& host,
                          std::string& path, int& port, bool& https)
{
    https = false;
    port = 80;

    std::string remaining = url;

    // Check for protocol
    if (StringUtils::StartsWith(remaining, "https://")) {
        https = true;
        port = 443;
        remaining = remaining.substr(8);
    } else if (StringUtils::StartsWith(remaining, "http://")) {
        remaining = remaining.substr(7);
    }

    // Find path separator
    size_t pathStart = remaining.find('/');
    if (pathStart == std::string::npos) {
        host = remaining;
        path = "/";
    } else {
        host = remaining.substr(0, pathStart);
        path = remaining.substr(pathStart);
    }

    // Check for port in host
    size_t colonPos = host.find(':');
    if (colonPos != std::string::npos) {
        port = std::stoi(host.substr(colonPos + 1));
        host = host.substr(0, colonPos);
    }

    return !host.empty();
}

HttpResponse HttpClient::Get(const std::string& url)
{
    return Get(url, {});
}

HttpResponse HttpClient::Get(const std::string& url,
                             const std::map<std::string, std::string>& headers)
{
    HttpResponse response;
    response.statusCode = 0;

    std::string host, path;
    int port;
    bool https;

    if (!ParseUrl(url, host, path, port, https)) {
        response.error = "Invalid URL";
        return response;
    }

    // Build HTTP request
    std::ostringstream requestStream;
    requestStream << "GET " << path << " HTTP/1.1\r\n";
    requestStream << "Host: " << host << "\r\n";
    requestStream << "User-Agent: MagicMirror/1.0\r\n";
    requestStream << "Accept: */*\r\n";
    requestStream << "Connection: close\r\n";

    for (const auto& [key, value] : headers) {
        requestStream << key << ": " << value << "\r\n";
    }

    requestStream << "\r\n";

    std::string request = requestStream.str();

    return DoRequest(host, port, request, https);
}

HttpResponse HttpClient::DoRequest(const std::string& host, int port,
                                   const std::string& request, bool https)
{
    HttpResponse response;
    response.statusCode = 0;

    // Resolve hostname
    CDNSClient dnsClient(m_pNet);
    CIPAddress serverIP;

    if (!dnsClient.Resolve(host.c_str(), &serverIP)) {
        response.error = "DNS resolution failed";
        return response;
    }

    // Create socket
    CSocket socket(m_pNet, IPPROTO_TCP);

    if (socket.Connect(serverIP, port) < 0) {
        response.error = "Connection failed";
        return response;
    }

    // For HTTPS, we'd need TLS support
    // This is a simplified implementation for HTTP only
    if (https) {
        response.error = "HTTPS not fully implemented - use HTTP for now";
        // In a real implementation, wrap socket with TLS here
        // For Circle OS, you'd use CTLSSimpleSupport or similar
    }

    // Send request
    if (socket.Send(request.c_str(), request.length(), 0) < 0) {
        response.error = "Send failed";
        return response;
    }

    // Receive response
    std::string responseData;
    char buffer[4096];
    int bytesReceived;

    while ((bytesReceived = socket.Receive(buffer, sizeof(buffer) - 1,
                                           MSG_DONTWAIT)) > 0) {
        buffer[bytesReceived] = '\0';
        responseData += buffer;

        // Yield to scheduler while waiting
        m_pScheduler->MsSleep(10);
    }

    // Parse response
    if (responseData.empty()) {
        response.error = "No response received";
        return response;
    }

    // Find header/body separator
    size_t headerEnd = responseData.find("\r\n\r\n");
    if (headerEnd == std::string::npos) {
        response.error = "Invalid HTTP response";
        return response;
    }

    std::string headerPart = responseData.substr(0, headerEnd);
    response.body = responseData.substr(headerEnd + 4);

    // Parse status line
    size_t firstLine = headerPart.find("\r\n");
    std::string statusLine = headerPart.substr(0, firstLine);

    // Parse "HTTP/1.1 200 OK"
    size_t statusStart = statusLine.find(' ');
    if (statusStart != std::string::npos) {
        size_t statusEnd = statusLine.find(' ', statusStart + 1);
        std::string statusStr = statusLine.substr(
            statusStart + 1, statusEnd - statusStart - 1);
        response.statusCode = std::stoi(statusStr);
    }

    // Parse headers
    auto headerLines = StringUtils::Split(
        headerPart.substr(firstLine + 2), "\r\n");

    for (const auto& line : headerLines) {
        size_t colonPos = line.find(':');
        if (colonPos != std::string::npos) {
            std::string key = StringUtils::Trim(line.substr(0, colonPos));
            std::string value = StringUtils::Trim(line.substr(colonPos + 1));
            response.headers[StringUtils::ToLower(key)] = value;
        }
    }

    // Handle chunked transfer encoding
    auto it = response.headers.find("transfer-encoding");
    if (it != response.headers.end() &&
        StringUtils::Contains(it->second, "chunked")) {
        // Decode chunked body
        std::string decodedBody;
        size_t pos = 0;

        while (pos < response.body.length()) {
            size_t lineEnd = response.body.find("\r\n", pos);
            if (lineEnd == std::string::npos) break;

            std::string chunkSizeStr = response.body.substr(pos, lineEnd - pos);
            int chunkSize = std::stoi(chunkSizeStr, nullptr, 16);

            if (chunkSize == 0) break;

            pos = lineEnd + 2;
            decodedBody += response.body.substr(pos, chunkSize);
            pos += chunkSize + 2;  // Skip chunk data and CRLF
        }

        response.body = decodedBody;
    }

    return response;
}

} // namespace mm
