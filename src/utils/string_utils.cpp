#include "utils/string_utils.h"
#include <algorithm>
#include <cctype>
#include <cstdarg>
#include <cstdio>
#include <sstream>

namespace mm {
namespace StringUtils {

std::string Trim(const std::string& str)
{
    size_t start = str.find_first_not_of(" \t\n\r");
    if (start == std::string::npos) {
        return "";
    }
    size_t end = str.find_last_not_of(" \t\n\r");
    return str.substr(start, end - start + 1);
}

std::string ToLower(const std::string& str)
{
    std::string result = str;
    std::transform(result.begin(), result.end(), result.begin(),
                   [](unsigned char c) { return std::tolower(c); });
    return result;
}

std::string ToUpper(const std::string& str)
{
    std::string result = str;
    std::transform(result.begin(), result.end(), result.begin(),
                   [](unsigned char c) { return std::toupper(c); });
    return result;
}

std::vector<std::string> Split(const std::string& str, char delimiter)
{
    std::vector<std::string> parts;
    std::istringstream stream(str);
    std::string part;

    while (std::getline(stream, part, delimiter)) {
        parts.push_back(part);
    }

    return parts;
}

std::vector<std::string> Split(const std::string& str,
                               const std::string& delimiter)
{
    std::vector<std::string> parts;
    size_t pos = 0;
    size_t prevPos = 0;

    while ((pos = str.find(delimiter, prevPos)) != std::string::npos) {
        parts.push_back(str.substr(prevPos, pos - prevPos));
        prevPos = pos + delimiter.length();
    }

    parts.push_back(str.substr(prevPos));
    return parts;
}

std::string Join(const std::vector<std::string>& parts,
                 const std::string& delimiter)
{
    std::string result;
    for (size_t i = 0; i < parts.size(); ++i) {
        if (i > 0) {
            result += delimiter;
        }
        result += parts[i];
    }
    return result;
}

bool StartsWith(const std::string& str, const std::string& prefix)
{
    if (prefix.length() > str.length()) {
        return false;
    }
    return str.compare(0, prefix.length(), prefix) == 0;
}

bool EndsWith(const std::string& str, const std::string& suffix)
{
    if (suffix.length() > str.length()) {
        return false;
    }
    return str.compare(str.length() - suffix.length(),
                       suffix.length(), suffix) == 0;
}

bool Contains(const std::string& str, const std::string& substr)
{
    return str.find(substr) != std::string::npos;
}

std::string Replace(const std::string& str, const std::string& from,
                    const std::string& to)
{
    std::string result = str;
    size_t pos = result.find(from);
    if (pos != std::string::npos) {
        result.replace(pos, from.length(), to);
    }
    return result;
}

std::string ReplaceAll(const std::string& str, const std::string& from,
                       const std::string& to)
{
    std::string result = str;
    size_t pos = 0;

    while ((pos = result.find(from, pos)) != std::string::npos) {
        result.replace(pos, from.length(), to);
        pos += to.length();
    }

    return result;
}

std::string Format(const char* fmt, ...)
{
    char buffer[1024];
    va_list args;
    va_start(args, fmt);
    vsnprintf(buffer, sizeof(buffer), fmt, args);
    va_end(args);
    return std::string(buffer);
}

std::string UrlEncode(const std::string& str)
{
    std::string result;
    result.reserve(str.length() * 3);

    for (unsigned char c : str) {
        if (std::isalnum(c) || c == '-' || c == '_' || c == '.' || c == '~') {
            result += c;
        } else if (c == ' ') {
            result += '+';
        } else {
            char buf[4];
            snprintf(buf, sizeof(buf), "%%%02X", c);
            result += buf;
        }
    }

    return result;
}

std::string UrlDecode(const std::string& str)
{
    std::string result;
    result.reserve(str.length());

    for (size_t i = 0; i < str.length(); ++i) {
        if (str[i] == '%' && i + 2 < str.length()) {
            int value;
            if (sscanf(str.substr(i + 1, 2).c_str(), "%x", &value) == 1) {
                result += static_cast<char>(value);
                i += 2;
            } else {
                result += str[i];
            }
        } else if (str[i] == '+') {
            result += ' ';
        } else {
            result += str[i];
        }
    }

    return result;
}

} // namespace StringUtils
} // namespace mm
