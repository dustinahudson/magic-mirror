#include "utils/json.h"
#include <sstream>
#include <cmath>
#include <stdexcept>

namespace mm {

const JsonValue JsonValue::s_null;
const JsonArray JsonValue::s_emptyArray;
const JsonObject JsonValue::s_emptyObject;
const std::string JsonValue::s_emptyString;

JsonValue::JsonValue() : m_value(nullptr) {}
JsonValue::JsonValue(std::nullptr_t) : m_value(nullptr) {}
JsonValue::JsonValue(bool value) : m_value(value) {}
JsonValue::JsonValue(int value) : m_value(static_cast<double>(value)) {}
JsonValue::JsonValue(double value) : m_value(value) {}
JsonValue::JsonValue(const char* value) : m_value(std::string(value)) {}
JsonValue::JsonValue(const std::string& value) : m_value(value) {}
JsonValue::JsonValue(const JsonArray& value) : m_value(value) {}
JsonValue::JsonValue(const JsonObject& value) : m_value(value) {}

bool JsonValue::IsNull() const {
    return std::holds_alternative<JsonNull>(m_value);
}

bool JsonValue::IsBool() const {
    return std::holds_alternative<JsonBool>(m_value);
}

bool JsonValue::IsNumber() const {
    return std::holds_alternative<JsonNumber>(m_value);
}

bool JsonValue::IsString() const {
    return std::holds_alternative<JsonString>(m_value);
}

bool JsonValue::IsArray() const {
    return std::holds_alternative<JsonArray>(m_value);
}

bool JsonValue::IsObject() const {
    return std::holds_alternative<JsonObject>(m_value);
}

bool JsonValue::AsBool(bool defaultValue) const {
    if (auto* v = std::get_if<JsonBool>(&m_value)) {
        return *v;
    }
    return defaultValue;
}

double JsonValue::AsNumber(double defaultValue) const {
    if (auto* v = std::get_if<JsonNumber>(&m_value)) {
        return *v;
    }
    return defaultValue;
}

int JsonValue::AsInt(int defaultValue) const {
    if (auto* v = std::get_if<JsonNumber>(&m_value)) {
        return static_cast<int>(*v);
    }
    return defaultValue;
}

const std::string& JsonValue::AsString(const std::string& defaultValue) const {
    if (auto* v = std::get_if<JsonString>(&m_value)) {
        return *v;
    }
    return defaultValue.empty() ? s_emptyString : defaultValue;
}

const JsonArray& JsonValue::AsArray() const {
    if (auto* v = std::get_if<JsonArray>(&m_value)) {
        return *v;
    }
    return s_emptyArray;
}

const JsonObject& JsonValue::AsObject() const {
    if (auto* v = std::get_if<JsonObject>(&m_value)) {
        return *v;
    }
    return s_emptyObject;
}

bool JsonValue::HasKey(const std::string& key) const {
    if (auto* obj = std::get_if<JsonObject>(&m_value)) {
        return obj->find(key) != obj->end();
    }
    return false;
}

const JsonValue& JsonValue::operator[](const std::string& key) const {
    if (auto* obj = std::get_if<JsonObject>(&m_value)) {
        auto it = obj->find(key);
        if (it != obj->end()) {
            return it->second;
        }
    }
    return s_null;
}

const JsonValue& JsonValue::operator[](size_t index) const {
    if (auto* arr = std::get_if<JsonArray>(&m_value)) {
        if (index < arr->size()) {
            return (*arr)[index];
        }
    }
    return s_null;
}

size_t JsonValue::Size() const {
    if (auto* arr = std::get_if<JsonArray>(&m_value)) {
        return arr->size();
    }
    if (auto* obj = std::get_if<JsonObject>(&m_value)) {
        return obj->size();
    }
    return 0;
}

// JSON Parser

JsonValue Json::Parse(const std::string& json) {
    std::string error;
    return Parse(json, error);
}

JsonValue Json::Parse(const std::string& json, std::string& error) {
    error.clear();
    size_t pos = 0;

    try {
        SkipWhitespace(json, pos);
        JsonValue result = ParseValue(json, pos);
        SkipWhitespace(json, pos);

        if (pos != json.length()) {
            error = "Unexpected characters after JSON value";
            return JsonValue();
        }

        return result;
    } catch (const std::exception& e) {
        error = e.what();
        return JsonValue();
    }
}

void Json::SkipWhitespace(const std::string& json, size_t& pos) {
    while (pos < json.length() &&
           (json[pos] == ' ' || json[pos] == '\t' ||
            json[pos] == '\n' || json[pos] == '\r')) {
        ++pos;
    }
}

JsonValue Json::ParseValue(const std::string& json, size_t& pos) {
    SkipWhitespace(json, pos);

    if (pos >= json.length()) {
        throw std::runtime_error("Unexpected end of JSON");
    }

    char c = json[pos];

    if (c == '{') {
        return ParseObject(json, pos);
    } else if (c == '[') {
        return ParseArray(json, pos);
    } else if (c == '"') {
        return ParseString(json, pos);
    } else if (c == '-' || (c >= '0' && c <= '9')) {
        return ParseNumber(json, pos);
    } else if (json.compare(pos, 4, "true") == 0) {
        pos += 4;
        return JsonValue(true);
    } else if (json.compare(pos, 5, "false") == 0) {
        pos += 5;
        return JsonValue(false);
    } else if (json.compare(pos, 4, "null") == 0) {
        pos += 4;
        return JsonValue(nullptr);
    }

    throw std::runtime_error("Invalid JSON value");
}

JsonValue Json::ParseObject(const std::string& json, size_t& pos) {
    JsonObject obj;

    ++pos;  // Skip '{'
    SkipWhitespace(json, pos);

    if (pos < json.length() && json[pos] == '}') {
        ++pos;
        return JsonValue(obj);
    }

    while (true) {
        SkipWhitespace(json, pos);

        // Parse key
        if (pos >= json.length() || json[pos] != '"') {
            throw std::runtime_error("Expected string key in object");
        }

        JsonValue keyValue = ParseString(json, pos);
        std::string key = keyValue.AsString();

        SkipWhitespace(json, pos);

        // Expect colon
        if (pos >= json.length() || json[pos] != ':') {
            throw std::runtime_error("Expected ':' after object key");
        }
        ++pos;

        // Parse value
        JsonValue value = ParseValue(json, pos);
        obj[key] = value;

        SkipWhitespace(json, pos);

        if (pos >= json.length()) {
            throw std::runtime_error("Unexpected end of object");
        }

        if (json[pos] == '}') {
            ++pos;
            break;
        } else if (json[pos] == ',') {
            ++pos;
        } else {
            throw std::runtime_error("Expected ',' or '}' in object");
        }
    }

    return JsonValue(obj);
}

JsonValue Json::ParseArray(const std::string& json, size_t& pos) {
    JsonArray arr;

    ++pos;  // Skip '['
    SkipWhitespace(json, pos);

    if (pos < json.length() && json[pos] == ']') {
        ++pos;
        return JsonValue(arr);
    }

    while (true) {
        JsonValue value = ParseValue(json, pos);
        arr.push_back(value);

        SkipWhitespace(json, pos);

        if (pos >= json.length()) {
            throw std::runtime_error("Unexpected end of array");
        }

        if (json[pos] == ']') {
            ++pos;
            break;
        } else if (json[pos] == ',') {
            ++pos;
        } else {
            throw std::runtime_error("Expected ',' or ']' in array");
        }
    }

    return JsonValue(arr);
}

JsonValue Json::ParseString(const std::string& json, size_t& pos) {
    ++pos;  // Skip opening quote

    std::string result;
    result.reserve(64);

    while (pos < json.length()) {
        char c = json[pos++];

        if (c == '"') {
            return JsonValue(result);
        } else if (c == '\\') {
            if (pos >= json.length()) {
                throw std::runtime_error("Unexpected end of string escape");
            }

            char escaped = json[pos++];
            switch (escaped) {
                case '"':  result += '"'; break;
                case '\\': result += '\\'; break;
                case '/':  result += '/'; break;
                case 'b':  result += '\b'; break;
                case 'f':  result += '\f'; break;
                case 'n':  result += '\n'; break;
                case 'r':  result += '\r'; break;
                case 't':  result += '\t'; break;
                case 'u': {
                    // Unicode escape
                    if (pos + 4 > json.length()) {
                        throw std::runtime_error("Invalid unicode escape");
                    }
                    // Simplified: just skip unicode escapes for now
                    pos += 4;
                    result += '?';
                    break;
                }
                default:
                    throw std::runtime_error("Invalid escape sequence");
            }
        } else {
            result += c;
        }
    }

    throw std::runtime_error("Unterminated string");
}

JsonValue Json::ParseNumber(const std::string& json, size_t& pos) {
    size_t start = pos;

    // Optional negative sign
    if (json[pos] == '-') {
        ++pos;
    }

    // Integer part
    if (pos < json.length() && json[pos] == '0') {
        ++pos;
    } else if (pos < json.length() && json[pos] >= '1' && json[pos] <= '9') {
        while (pos < json.length() && json[pos] >= '0' && json[pos] <= '9') {
            ++pos;
        }
    } else {
        throw std::runtime_error("Invalid number");
    }

    // Fractional part
    if (pos < json.length() && json[pos] == '.') {
        ++pos;
        if (pos >= json.length() || json[pos] < '0' || json[pos] > '9') {
            throw std::runtime_error("Invalid number");
        }
        while (pos < json.length() && json[pos] >= '0' && json[pos] <= '9') {
            ++pos;
        }
    }

    // Exponent
    if (pos < json.length() && (json[pos] == 'e' || json[pos] == 'E')) {
        ++pos;
        if (pos < json.length() && (json[pos] == '+' || json[pos] == '-')) {
            ++pos;
        }
        if (pos >= json.length() || json[pos] < '0' || json[pos] > '9') {
            throw std::runtime_error("Invalid number exponent");
        }
        while (pos < json.length() && json[pos] >= '0' && json[pos] <= '9') {
            ++pos;
        }
    }

    std::string numStr = json.substr(start, pos - start);
    return JsonValue(std::stod(numStr));
}

std::string Json::Stringify(const JsonValue& value, bool pretty) {
    std::ostringstream ss;

    if (value.IsNull()) {
        ss << "null";
    } else if (value.IsBool()) {
        ss << (value.AsBool() ? "true" : "false");
    } else if (value.IsNumber()) {
        ss << value.AsNumber();
    } else if (value.IsString()) {
        ss << '"';
        for (char c : value.AsString()) {
            switch (c) {
                case '"':  ss << "\\\""; break;
                case '\\': ss << "\\\\"; break;
                case '\b': ss << "\\b"; break;
                case '\f': ss << "\\f"; break;
                case '\n': ss << "\\n"; break;
                case '\r': ss << "\\r"; break;
                case '\t': ss << "\\t"; break;
                default:   ss << c; break;
            }
        }
        ss << '"';
    } else if (value.IsArray()) {
        ss << '[';
        const auto& arr = value.AsArray();
        for (size_t i = 0; i < arr.size(); ++i) {
            if (i > 0) ss << ',';
            ss << Stringify(arr[i], pretty);
        }
        ss << ']';
    } else if (value.IsObject()) {
        ss << '{';
        const auto& obj = value.AsObject();
        bool first = true;
        for (const auto& [key, val] : obj) {
            if (!first) ss << ',';
            first = false;
            ss << '"' << key << "\":" << Stringify(val, pretty);
        }
        ss << '}';
    }

    return ss.str();
}

} // namespace mm
